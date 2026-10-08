package protocol

const (
	TemplateRuntimeKindDocker      = "docker"
	TemplateRuntimeKindModelRunner = "model_runner"
)

type Template struct {
	ID                   int      `json:"id"`
	IsMine               bool     `json:"is_mine"`
	TemplateName         string   `json:"template_name"`
	TemplateDescription  string   `json:"template_description"`
	RuntimeKind          string   `json:"runtime_kind,omitempty"`
	DefaultModel         string   `json:"default_model,omitempty"`
	AutoPullDefaultModel bool     `json:"auto_pull_default_model,omitempty"`
	AllowRuntimeModelOps bool     `json:"allow_runtime_model_ops,omitempty"`
	OnStartScript        string   `json:"on_start_script"`
	ExtraFilters         []string `json:"extra_filters"`
	VRAMRequiredGB       int      `json:"vram_required_gb"`
	DockerServerName     string   `json:"docker_server_name"`
	DockerUsername       string   `json:"docker_username"`
	DockerPassword       string   `json:"docker_password"`
	DiskSpaceMB          int      `json:"disk_space_mb"`
	IsPrivate            bool     `json:"is_private"`
	CampaignTemplate     bool     `json:"campaign_template"`
	IsCampaignFork       bool     `json:"is_campaign_fork"`
	ForkedFromTemplateID int      `json:"forked_from_template_id"`
	ImagePath            string   `json:"image_path"`
	DockerOptions        string   `json:"docker_options"`
	// Ports format: "containerPort[/proto]" or "hostPort:containerPort[/proto]".
	// proto: tcp|udp|both, default when omitted is tcp (Docker default).
	Ports []string `json:"ports"`
	// Parsed representation of Ports for control-plane consumers.
	// For writes, either `ports` or `port_mappings` may be provided.
	PortMappings         []PortMappingSpec `json:"port_mappings,omitempty"`
	EnvironmentVariables []string          `json:"environment_variables"`
	Readme               string            `json:"readme"`
}
