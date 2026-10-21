// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"os"
	"sort"
	"strings"
)

// InheritedEnvironment captures Core's environment at the process boundary.
// Callers name any additional provider credential variables they configured.
func InheritedEnvironment(providerVariables ...string) []string {
	return Environment(os.Environ(), providerVariables...)
}

// Environment preserves the user's tool setup, excluding model-provider
// credentials and variables that inject code before confinement is installed.
// Duplicate entries use the last value. A nil input is an empty environment.
func Environment(input []string, providerVariables ...string) []string {
	denied := map[string]bool{"GOOGLE_API_KEY": true, "GOOGLE_APPLICATION_CREDENTIALS": true, "BASH_ENV": true, "ENV": true, "GCONV_PATH": true}
	for _, name := range providerVariables {
		denied[strings.ToUpper(name)] = true
	}
	values := map[string]string{}
	for _, entry := range input {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" || strings.ContainsRune(entry, 0) {
			continue
		}
		upper := strings.ToUpper(name)
		blocked := denied[upper]
		for _, prefix := range []string{"OPENAI_", "ANTHROPIC_", "CLAUDE_", "GEMINI_", "GROK_", "XAI_", "AZURE_OPENAI_", "COHERE_", "MISTRAL_", "GROQ_", "DEEPSEEK_", "OPENROUTER_", "LD_", "DYLD_"} {
			blocked = blocked || strings.HasPrefix(upper, prefix)
		}
		if !blocked {
			values[name] = value
		}
	}
	result := make([]string, 0, len(values))
	for name, value := range values {
		result = append(result, name+"="+value)
	}
	sort.Strings(result)
	return result
}
