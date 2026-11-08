package main

import "github.com/Autumn-27/artex/server"

func (c launchConfig) modelOptions() server.FirstDiagnosisModelOptions {
	return server.FirstDiagnosisModelOptions{Mode: c.ModelExecutionMode, LocalManifestPath: c.LocalLLMManifestPath, OpenRouterManifestPath: c.OpenRouterManifestPath}
}
