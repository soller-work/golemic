package agent

import (
	"testing"
)

func TestParseModelChain(t *testing.T) { //nolint:funlen
	tests := []struct {
		name    string
		input   string
		want    []string
		wantErr bool
	}{
		{
			name:  "single model unchanged",
			input: "claude-bridge/claude-opus-4-8",
			want:  []string{"claude-bridge/claude-opus-4-8"},
		},
		{
			name:  "two models",
			input: "claude-bridge/claude-opus-4-8, openrouter/minimax/minimax-m3",
			want:  []string{"claude-bridge/claude-opus-4-8", "openrouter/minimax/minimax-m3"},
		},
		{
			name:  "trims whitespace",
			input: "  model-a  ,  model-b  ",
			want:  []string{"model-a", "model-b"},
		},
		{
			name:  "drops empty entries",
			input: "model-a,,model-b,",
			want:  []string{"model-a", "model-b"},
		},
		{
			name:  "deduplicates keeping first occurrence",
			input: "model-a,model-b,model-a",
			want:  []string{"model-a", "model-b"},
		},
		{
			name:  "spaces empty and duplicates combined",
			input: "  model-a , , model-b , model-a ,",
			want:  []string{"model-a", "model-b"},
		},
		{
			name:    "empty string is error",
			input:   "",
			wantErr: true,
		},
		{
			name:    "only commas is error",
			input:   ",,,",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseModelChain(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseModelChain(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
