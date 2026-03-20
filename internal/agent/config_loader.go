package agent

import (
	"encoding/json"
	"fmt"
	"os"
)

// LoadConfigs reads agent configurations from a JSON file.
// The file must contain a JSON array of Config objects.
// Returns the parsed configs, or an error if the file cannot be read or parsed.
func LoadConfigs(path string) ([]Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agent config: %w", err)
	}

	var configs []Config
	if err := json.Unmarshal(data, &configs); err != nil {
		return nil, fmt.Errorf("parse agent config: %w", err)
	}

	for i, c := range configs {
		if c.ID == "" {
			return nil, fmt.Errorf("agent config[%d]: missing id", i)
		}
		if c.Role == "" {
			return nil, fmt.Errorf("agent config[%d] (%s): missing role", i, c.ID)
		}
		if c.Model == "" {
			return nil, fmt.Errorf("agent config[%d] (%s): missing model", i, c.ID)
		}
	}

	return configs, nil
}
