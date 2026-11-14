package exercise

import (
	"encoding/json"
	"os"
	"testing"

	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
)

// The golden cases are those of the page's own tests of descriptionValues (see the file's "source"): the same placeholders
// and labs must give the same values here.
func TestPlaceholderValuesMatchThePagesRules(t *testing.T) {
	raw, err := os.ReadFile("testdata/placeholder_values.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Cases []struct {
			Name string `json:"name"`
			Lab  struct {
				Ready    bool `json:"ready"`
				Gateways []struct {
					Name string `json:"name"`
					CIDR string `json:"cidr"`
				} `json:"gateways"`
				Access []struct {
					Device   string `json:"device"`
					Port     int32  `json:"port"`
					Protocol string `json:"protocol"`
					URL      string `json:"url"`
				} `json:"access"`
			} `json:"lab"`
			Placeholders []exerciseModel.Placeholder `json:"placeholders"`
			Want         map[string]string           `json:"want"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden.Cases) < 7 {
		t.Fatalf("the golden file lost cases: %d", len(golden.Cases))
	}
	for _, c := range golden.Cases {
		t.Run(c.Name, func(t *testing.T) {
			status := exerciseModel.LabDeployStatus{Ready: c.Lab.Ready}
			for _, g := range c.Lab.Gateways {
				status.Gateways = append(status.Gateways, exerciseModel.LabDeployedGateway{Name: g.Name, CIDR: g.CIDR})
			}
			for _, a := range c.Lab.Access {
				status.Access = append(status.Access, exerciseModel.LabAccess{Device: a.Device, Port: a.Port, Protocol: a.Protocol, URL: a.URL})
			}
			got := placeholderValues(c.Placeholders, status)
			if len(got) != len(c.Want) {
				t.Fatalf("%v, want %v", got, c.Want)
			}
			for key, want := range c.Want {
				if got[key] != want {
					t.Errorf("%s = %q, want %q", key, got[key], want)
				}
			}
		})
	}
}
