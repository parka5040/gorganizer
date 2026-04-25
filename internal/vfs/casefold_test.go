package vfs

import "testing"

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"Textures", "textures"},
		{"Textures/Sky/SKY.dds", "textures/sky/sky.dds"},
		{"MESHES/Actors/", "meshes/actors"},
		{"/leading/slash", "leading/slash"},
		{"trailing/", "trailing"},
		{"Mixed\\Backslash\\Path", "mixed/backslash/path"},
		{"Interface/SKSE/Plugins.txt", "interface/skse/plugins.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizePath(tt.input)
			if got != tt.want {
				t.Errorf("NormalizePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"SkyUI_SE.esp", "skyui_se.esp"},
		{"TEXTURE.DDS", "texture.dds"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeName(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestJoinVPath(t *testing.T) {
	tests := []struct {
		parent string
		child  string
		want   string
	}{
		{"", "Textures", "textures"},
		{"textures", "SKY.dds", "textures/sky.dds"},
		{"textures/sky", "Cloud.dds", "textures/sky/cloud.dds"},
	}

	for _, tt := range tests {
		t.Run(tt.parent+"/"+tt.child, func(t *testing.T) {
			got := JoinVPath(tt.parent, tt.child)
			if got != tt.want {
				t.Errorf("JoinVPath(%q, %q) = %q, want %q", tt.parent, tt.child, got, tt.want)
			}
		})
	}
}

func TestCleanVPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"/", ""},
		{"foo/", "foo"},
		{"/foo/bar/", "foo/bar"},
		{"foo\\bar", "foo/bar"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := CleanVPath(tt.input)
			if got != tt.want {
				t.Errorf("CleanVPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
