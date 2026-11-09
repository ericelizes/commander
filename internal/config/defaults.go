package config

// Defaults returns a fresh Config populated with the values commander assumes
// when the user has not customized anything. These mirror the schema documented
// in the U4 plan and ~/.commander/config.toml example.
//
// Callers must not mutate the returned struct's nested maps in a way that
// affects future Defaults() calls — Projects is initialized empty.
func Defaults() *Config {
	return &Config{
		Concurrency: Concurrency{
			MaxOperatorsPerWorkstream: 8,
		},
		Ceilings: Ceilings{
			MaxUSDPerOperator:    5.0,
			MaxTokensPerOperator: 200000,
			MaxWallclockMinutes:  60,
			MaxRetries:           3,
		},
		Models: Models{
			DefaultCommander: "anthropic/claude-opus-4-7",
			DefaultLead:      "anthropic/claude-sonnet-4-7",
			DefaultOperator:  "anthropic/claude-sonnet-4-7",
		},
		Runtime: Runtime{
			Default: "opencode",
		},
		Projects: map[string]Project{},
	}
}
