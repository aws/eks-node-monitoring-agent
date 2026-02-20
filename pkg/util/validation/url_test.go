package validation

import (
	"testing"
)

func TestParseURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "Valid URL",
			input:   "https://example.com",
			wantErr: false,
		},
		{
			name:    "Invalid hostname characters",
			input:   "https://evil.org_.!;$",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseURL(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseURL(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestParseAPIServerURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{
			name:    "Valid API server URL",
			url:     "https://api.example.com",
			wantErr: false,
		},
		{
			name:    "Invalid scheme",
			url:     "http://api.example.com",
			wantErr: true,
		},
		{
			name:    "Invalid hostname characters",
			url:     "https://api_.example.com",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseAPIServerURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseAPIServerURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestParseProviderID(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		wantID     string
		wantErr    bool
	}{
		{
			name:       "Valid provider ID",
			providerID: "aws:///eu-west-1a/i-0cb3f1ceeb038fb6c",
			wantID:     "i-0cb3f1ceeb038fb6c",
			wantErr:    false,
		},
		{
			name:       "Invalid scheme",
			providerID: "gcp:///zone/instance",
			wantErr:    true,
		},
		{
			name:       "Missing prefix",
			providerID: "aws://instance",
			wantErr:    true,
		},
		{
			name:       "Invalid instance ID format",
			providerID: "aws:///eu-west-1a/wrong-format",
			wantErr:    true,
		},
		{
			name:       "Too many path segments",
			providerID: "aws:///region/zone/instance/extra",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, err := ParseProviderID(tt.providerID)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseProviderID(%q) error = %v, wantErr %v", tt.providerID, err, tt.wantErr)
				return
			}
			if !tt.wantErr && gotID != tt.wantID {
				t.Errorf("ParseProviderID(%q) = %v, want %v", tt.providerID, gotID, tt.wantID)
			}
		})
	}
}
