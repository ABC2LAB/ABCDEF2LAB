//go:build !linux

package llmpolicy

func LoadOpenRouterManifestFile(string) (*OpenRouterPolicy, error) { return nil, ErrOpenRouterPolicy }

func LoadOpenRouterManifestFileForRun(string, string) (*OpenRouterPolicy, error) {
	return nil, ErrOpenRouterPolicy
}
