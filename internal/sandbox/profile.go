package sandbox

import (
	"os"
	"path/filepath"
	"strings"
)

// Profile es una receta de ejecución por ecosistema.
type Profile struct {
	Name    string
	Image   string
	Command []string
}

// Perfiles incorporados. Con la red en none las dependencias no se pueden
// descargar: quien llama decide si pide red (Spec.Network).
var (
	ProfileMaven = Profile{
		Name:    "maven",
		Image:   "maven:3-eclipse-temurin-21",
		Command: []string{"mvn", "spring-boot:run"},
	}
	ProfileGradle = Profile{
		Name:    "gradle",
		Image:   "gradle:jdk21",
		Command: []string{"gradle", "bootRun"},
	}
	ProfileDotnet = Profile{
		Name:    "dotnet",
		Image:   "mcr.microsoft.com/dotnet/sdk:8.0",
		Command: []string{"dotnet", "run"},
	}
)

// Profiles devuelve los perfiles en orden de prioridad de detección.
func Profiles() []Profile { return []Profile{ProfileMaven, ProfileGradle, ProfileDotnet} }

// ProfileByName busca un perfil por nombre.
func ProfileByName(name string) (Profile, bool) {
	for _, p := range Profiles() {
		if p.Name == name {
			return p, true
		}
	}
	return Profile{}, false
}

// DetectProfile mira la raíz del proyecto: pom.xml es Maven,
// build.gradle(.kts) es Gradle y un *.csproj es dotnet.
func DetectProfile(root string) (Profile, bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return Profile{}, false
	}
	var maven, gradle, dotnet bool
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch n := e.Name(); {
		case n == "pom.xml":
			maven = true
		case n == "build.gradle" || n == "build.gradle.kts":
			gradle = true
		case strings.EqualFold(filepath.Ext(n), ".csproj"):
			dotnet = true
		}
	}
	switch {
	case maven:
		return ProfileMaven, true
	case gradle:
		return ProfileGradle, true
	case dotnet:
		return ProfileDotnet, true
	}
	return Profile{}, false
}
