package hw

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	ghwgpu "github.com/jaypipes/ghw/pkg/gpu"
	"github.com/jaypipes/pcidb"
)

type runtimeGPU struct {
	Index      int
	PCIAddress string
	UUID       string
	Name       string
	VRAMMB     uint64
	ComputeAPI string
}

func reconcileRuntimeGPUs(cards []*ghwgpu.GraphicsCard, runtimeGPUs []runtimeGPU) []RentableGPU {
	cardsByAddress := make(map[string]*ghwgpu.GraphicsCard, len(cards))
	nvidiaCards := make([]*ghwgpu.GraphicsCard, 0, len(cards))
	for _, card := range cards {
		if card == nil {
			continue
		}
		if isNVIDIAGraphicsCard(card) {
			nvidiaCards = append(nvidiaCards, card)
		}
		address := normalizePCIAddress(card.Address)
		if address == "" && card.DeviceInfo != nil {
			address = normalizePCIAddress(card.DeviceInfo.Address)
		}
		if address != "" {
			cardsByAddress[address] = card
		}
	}

	sort.SliceStable(runtimeGPUs, func(i, j int) bool {
		return runtimeGPUs[i].Index < runtimeGPUs[j].Index
	})

	out := make([]RentableGPU, 0, len(runtimeGPUs))
	seenAddresses := make(map[string]struct{}, len(runtimeGPUs))
	usedCards := make(map[*ghwgpu.GraphicsCard]struct{}, len(runtimeGPUs))
	for _, device := range runtimeGPUs {
		address := normalizePCIAddress(device.PCIAddress)
		if address == "" || device.VRAMMB == 0 || !isCUDAComputeAPI(device.ComputeAPI) {
			continue
		}
		if _, exists := seenAddresses[address]; exists {
			continue
		}

		card := cardsByAddress[address]
		if card == nil {
			card = matchNVIDIACardByRuntimeName(nvidiaCards, usedCards, device.Name)
		}
		if !isNVIDIAGraphicsCard(card) {
			continue
		}
		if _, used := usedCards[card]; used {
			continue
		}

		resolvedCard := cloneGraphicsCard(card)
		resolvedCard.Index = len(out)
		resolvedCard.Address = address
		if resolvedCard.DeviceInfo != nil {
			resolvedCard.DeviceInfo.Address = address
			resolvedName := strings.TrimSpace(device.Name)
			if resolvedName != "" && shouldOverrideGPUProductName(productName(resolvedCard)) {
				if resolvedCard.DeviceInfo.Product == nil {
					resolvedCard.DeviceInfo.Product = &pcidb.Product{}
				}
				resolvedCard.DeviceInfo.Product.Name = resolvedName
			}
		}

		out = append(out, RentableGPU{
			Card:         resolvedCard,
			Runtime:      "nvidia",
			RuntimeIndex: device.Index,
			UUID:         strings.TrimSpace(device.UUID),
			PCIAddress:   address,
			VRAMMB:       device.VRAMMB,
			ComputeAPI:   strings.TrimSpace(device.ComputeAPI),
		})
		seenAddresses[address] = struct{}{}
		usedCards[card] = struct{}{}
	}

	return out
}

func matchNVIDIACardByRuntimeName(cards []*ghwgpu.GraphicsCard, used map[*ghwgpu.GraphicsCard]struct{}, runtimeName string) *ghwgpu.GraphicsCard {
	normalizedRuntimeName := normalizeGPUName(runtimeName)
	var matching []*ghwgpu.GraphicsCard
	var unused []*ghwgpu.GraphicsCard
	for _, card := range cards {
		if _, exists := used[card]; exists {
			continue
		}
		unused = append(unused, card)
		cardName := normalizeGPUName(productName(card))
		if normalizedRuntimeName != "" && cardName != "" &&
			(cardName == normalizedRuntimeName || strings.Contains(cardName, normalizedRuntimeName) || strings.Contains(normalizedRuntimeName, cardName)) {
			matching = append(matching, card)
		}
	}
	if len(matching) == 1 {
		return matching[0]
	}
	if len(unused) == 1 {
		return unused[0]
	}
	if len(matching) > 1 {
		// Identical NVIDIA models are equivalent for capacity accounting; retain
		// runtime order while still excluding every non-NVIDIA display adapter.
		return matching[0]
	}
	return nil
}

func applyRentableGPUs(specs *SystemSpecs, rentable []RentableGPU) {
	if specs == nil {
		return
	}

	specs.RentableGPUs = append(make([]RentableGPU, 0, len(rentable)), rentable...)
	specs.GraphicsCards = make([]*ghwgpu.GraphicsCard, 0, len(rentable))
	specs.GPUVRAMMB = make([]uint64, 0, len(rentable))
	specs.ComputeAPIs = make([]string, 0, len(rentable))
	for _, device := range rentable {
		specs.GraphicsCards = append(specs.GraphicsCards, device.Card)
		specs.GPUVRAMMB = append(specs.GPUVRAMMB, device.VRAMMB)
		specs.ComputeAPIs = append(specs.ComputeAPIs, device.ComputeAPI)
	}
}

func queryNvidiaSMIGPUs() ([]runtimeGPU, error) {
	cmd := exec.Command(
		"nvidia-smi",
		"--query-gpu=index,pci.bus_id,uuid,name,memory.total,compute_cap",
		"--format=csv,noheader,nounits",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseNvidiaSMIGPUs(out)
}

func parseNvidiaSMIGPUs(raw []byte) ([]runtimeGPU, error) {
	reader := csv.NewReader(bytes.NewReader(raw))
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = 6
	devices := make([]runtimeGPU, 0)
	for {
		parts, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse nvidia-smi row: %w", err)
		}

		index, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, fmt.Errorf("parse nvidia-smi index: %w", err)
		}
		memoryDigits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, parts[4])
		memoryMB, err := strconv.ParseUint(memoryDigits, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse nvidia-smi memory: %w", err)
		}

		computeCapability := strings.TrimSpace(parts[5])
		devices = append(devices, runtimeGPU{
			Index:      index,
			PCIAddress: strings.TrimSpace(parts[1]),
			UUID:       strings.TrimSpace(parts[2]),
			Name:       strings.TrimSpace(parts[3]),
			VRAMMB:     memoryMB,
			ComputeAPI: "CUDA " + computeCapability,
		})
	}
	return devices, nil
}

func normalizePCIAddress(raw string) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if len(normalized) >= 12 && strings.Count(normalized, ":") >= 2 {
		return normalized[len(normalized)-12:]
	}
	return normalized
}

func normalizeGPUName(raw string) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	normalized = strings.TrimPrefix(normalized, "nvidia corporation ")
	normalized = strings.TrimPrefix(normalized, "nvidia ")
	return strings.Join(strings.Fields(normalized), " ")
}

func shouldOverrideGPUProductName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" || normalized == "unknown" {
		return true
	}
	if strings.HasPrefix(normalized, "device ") {
		return true
	}
	if strings.HasPrefix(normalized, "nvidia device ") {
		return true
	}
	if strings.HasPrefix(normalized, "nvidia corporation device ") {
		return true
	}
	return false
}

func isNVIDIAGraphicsCard(card *ghwgpu.GraphicsCard) bool {
	if card == nil || card.DeviceInfo == nil {
		return false
	}
	if card.DeviceInfo.Vendor != nil {
		if strings.EqualFold(strings.TrimSpace(card.DeviceInfo.Vendor.ID), "10de") {
			return true
		}
		if strings.Contains(strings.ToLower(card.DeviceInfo.Vendor.Name), "nvidia") {
			return true
		}
	}
	return strings.Contains(strings.ToLower(productName(card)), "nvidia")
}

func isCUDAComputeAPI(raw string) bool {
	parts := strings.Fields(strings.TrimSpace(raw))
	if len(parts) < 2 || !strings.EqualFold(parts[0], "cuda") {
		return false
	}
	_, err := strconv.ParseFloat(parts[len(parts)-1], 64)
	return err == nil
}

func productName(card *ghwgpu.GraphicsCard) string {
	if card == nil || card.DeviceInfo == nil || card.DeviceInfo.Product == nil {
		return ""
	}
	return card.DeviceInfo.Product.Name
}

func cloneGraphicsCard(card *ghwgpu.GraphicsCard) *ghwgpu.GraphicsCard {
	cloned := *card
	if card.DeviceInfo != nil {
		deviceInfo := *card.DeviceInfo
		cloned.DeviceInfo = &deviceInfo
		if card.DeviceInfo.Product != nil {
			product := *card.DeviceInfo.Product
			cloned.DeviceInfo.Product = &product
		}
	}
	return &cloned
}
