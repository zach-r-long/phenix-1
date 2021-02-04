package v1

type RoleSpec struct {
	Name     string        `yaml:"roleNname" json:"roleName" structs:"roleName" mapstructure:"roleName"`
	Policies []*PolicySpec `yaml:"policies" json:"policies" structs:"policies" mapstructure:"policies"`
}

type PolicySpec struct {
	Experiments   []string `yaml:"experiments" json:"experiments" structs:"experiments" mapstructure:"experiments"`
	Resources     []string `yaml:"resources" json:"resources" structs:"resources" mapstructure:"resources"`
	ResourceNames []string `yaml:"resourceNames" json:"resourceNames" structs:"resourceNames" mapstructure:"resourceNames"`
	Verbs         []string `yaml:"verbs" json:"verbs" structs:"verbs" mapstructure:"verbs"`
}
