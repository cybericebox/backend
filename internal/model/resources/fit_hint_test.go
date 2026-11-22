package resourcesModel

import "testing"

func TestFitHintClampsToTheFrameAndRefusesWhatCannotWork(t *testing.T) {
	p := DefaultPolicy() // frame: large 1Gi/250m
	mi := int64(1 << 20)
	for name, c := range map[string]struct {
		hint Amount
		want string
		ok   bool
	}{
		"no hint: the default preset":              {Amount{}, "micro", true},
		"a small hint: the smallest that holds it": {Amount{CPUMillicores: 20, MemoryBytes: 100 * mi}, "small", true},
		"exactly the frame":                        {Amount{CPUMillicores: 250, MemoryBytes: 1024 * mi}, "large", true},
		"above the frame, workable: clamped":       {Amount{CPUMillicores: 500, MemoryBytes: 2048 * mi}, "large", true},
		"memory far above the frame":               {Amount{CPUMillicores: 100, MemoryBytes: 4096 * mi}, "", false},
		"cpu far above the frame":                  {Amount{CPUMillicores: 1000, MemoryBytes: 512 * mi}, "", false},
	} {
		got, ok := p.FitHint(c.hint)
		if ok != c.ok || (ok && got.ID != c.want) {
			t.Errorf("%s: %q %v", name, got.ID, ok)
		}
	}
}
