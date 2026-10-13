package smklog

// Guards the client against the vendored contract: an operation or a model
// the OpenAPI document declares must exist here, so a refresh of testdata/
// that adds one fails loudly instead of shipping an SDK that quietly lacks it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

type openAPIDocument struct {
	Servers []struct {
		URL string `json:"url"`
	} `json:"servers"`
	Paths map[string]map[string]struct {
		OperationID string `json:"operationId"`
	} `json:"paths"`
	Components struct {
		Schemas map[string]json.RawMessage `json:"schemas"`
	} `json:"components"`
}

func loadOpenAPI(t *testing.T) openAPIDocument {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc openAPIDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

var httpMethods = map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}

// Go spells an operationId with a leading capital: getParcelQuote is GetParcelQuote.
func methodName(operationID string) string {
	return strings.ToUpper(operationID[:1]) + operationID[1:]
}

func TestEveryOperationHasAMethod(t *testing.T) {
	doc := loadOpenAPI(t)
	clientType := reflect.TypeOf(&Client{})
	found := 0
	for path, item := range doc.Paths {
		for method, op := range item {
			if !httpMethods[method] {
				continue
			}
			found++
			if op.OperationID == "" {
				t.Fatalf("%s %s has no operationId", strings.ToUpper(method), path)
			}
			if _, ok := clientType.MethodByName(methodName(op.OperationID)); !ok {
				t.Errorf("%s %s is declared as %s but *Client has no %s method", strings.ToUpper(method), path, op.OperationID, methodName(op.OperationID))
			}
		}
	}
	if found < 4 {
		t.Fatalf("expected at least the four known operations, found %d", found)
	}
}

func TestEveryComponentSchemaHasAType(t *testing.T) {
	doc := loadOpenAPI(t)
	types := map[string]reflect.Type{
		"QuoteRequest":           reflect.TypeOf(QuoteRequest{}),
		"QuoteResponse":          reflect.TypeOf(QuoteResponse{}),
		"Rate":                   reflect.TypeOf(Rate{}),
		"Package":                reflect.TypeOf(Package{}),
		"CheckoutSessionRequest": reflect.TypeOf(CheckoutSessionRequest{}),
		"CheckoutSession":        reflect.TypeOf(CheckoutSession{}),
		"CheckoutStatus":         reflect.TypeOf(CheckoutStatus{}),
		"Liveness":               reflect.TypeOf(Liveness{}),
		"Problem":                reflect.TypeOf(Problem{}),
	}
	for name, raw := range doc.Components.Schemas {
		typ, ok := types[name]
		if !ok {
			t.Errorf("schema %s has no Go type in this package", name)
			continue
		}
		// Every property the schema names is a tagged field, spelled as on the wire.
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		tagged := wireNames(typ)
		for property := range schema.Properties {
			if !tagged[property] {
				t.Errorf("schema %s names %q but %s has no field with that json tag", name, property, typ.Name())
			}
		}
	}
}

func TestDefaultBaseURLIsTheContractServer(t *testing.T) {
	doc := loadOpenAPI(t)
	if len(doc.Servers) == 0 || New().BaseURL() != doc.Servers[0].URL {
		t.Fatalf("DefaultBaseURL = %q, contract servers = %+v", DefaultBaseURL, doc.Servers)
	}
}

func TestVersionMovesWithTheOtherClients(t *testing.T) {
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(Version) {
		t.Fatalf("Version %q is not semver", Version)
	}
	raw, err := os.ReadFile(filepath.Join("..", "javascript", "package.json"))
	if err != nil {
		t.Skip("javascript/package.json not beside this module")
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	if pkg.Version != Version {
		t.Fatalf("Go Version %q, npm package %q: the three SDKs release the same version together", Version, pkg.Version)
	}
}
