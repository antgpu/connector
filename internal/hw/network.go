package hw

import (
	"log"

	"github.com/jaypipes/ghw"
	psNet "github.com/shirou/gopsutil/v4/net"
)

func FetchNetworkInfo(s *SystemSpecs) {
	var err error
	interfaces, err := psNet.Interfaces()
	if err != nil {
		s.NetworkPsutilFetchErr = err.Error()
		log.Println(err)
	}
	s.NetworkPSUtil = interfaces

	s.Network, err = ghw.Network()
	if err != nil {
		s.NetworkFetchErr = err.Error()
		log.Println(err)
	}
}
