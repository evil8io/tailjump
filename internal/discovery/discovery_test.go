package discovery

import (
	"encoding/base64"
	"errors"
	"testing"
)

func TestParseValidDocument(t *testing.T) {
	doc := `{
		"version": "1",
		"hostname": "gw.example",
		"uname_m": "aarch64",
		"exec_dir": "/root/.cache/tj",
		"manifest_path": "/etc/tj/manifest.yaml",
		"manifest": "` + base64.StdEncoding.EncodeToString([]byte("version: 1\n")) + `",
		"addresses": ["10.0.0.10/16"],
		"link_routes": ["10.0.0.0/20"],
		"resolvers": ["10.0.0.2"],
		"search_domains": ["corp.example"],
		"cloud": {"provider": "aws", "networks": ["10.0.0.0/16", "2001:db8::/56"]}
	}`

	res, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.Hostname != "gw.example" || res.ExecDir != "/root/.cache/tj" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Cloud == nil || res.Cloud.Provider != "aws" || len(res.Cloud.Networks) != 2 {
		t.Fatalf("cloud: %+v", res.Cloud)
	}

	m, err := res.DecodedManifest()
	if err != nil {
		t.Fatalf("DecodedManifest: %v", err)
	}
	if string(m) != "version: 1\n" {
		t.Fatalf("manifest: %q", m)
	}

	routes, err := res.LinkRoutePrefixes()
	if err != nil || len(routes) != 1 {
		t.Fatalf("LinkRoutePrefixes: %v %v", routes, err)
	}
	nets, err := res.CloudNetworkPrefixes()
	if err != nil || len(nets) != 2 {
		t.Fatalf("CloudNetworkPrefixes: %v %v", nets, err)
	}
}

func TestParseResultWithNoManifestOrCloud(t *testing.T) {
	doc := `{
		"version": "1",
		"hostname": "gw.example",
		"uname_m": "x86_64",
		"exec_dir": "/root/.cache/tj",
		"addresses": [],
		"link_routes": [],
		"resolvers": [],
		"search_domains": []
	}`
	res, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.ManifestPath != "" || res.Manifest != "" || res.Cloud != nil {
		t.Fatalf("want an absent manifest and cloud, got %+v", res)
	}
	m, err := res.DecodedManifest()
	if err != nil || m != nil {
		t.Fatalf("DecodedManifest: %v %v", m, err)
	}
	nets, err := res.CloudNetworkPrefixes()
	if err != nil || nets != nil {
		t.Fatalf("CloudNetworkPrefixes: %v %v", nets, err)
	}
}

func TestParseRejectsMissingVersion(t *testing.T) {
	doc := `{"hostname":"gw","uname_m":"x86_64","exec_dir":"/tmp","addresses":[],"link_routes":[],"resolvers":[],"search_domains":[]}`
	if _, err := Parse([]byte(doc)); err == nil {
		t.Fatal("want error for a missing version")
	}
}

func TestParseRejectsMissingExecDir(t *testing.T) {
	doc := `{"version":"1","hostname":"gw","uname_m":"x86_64","exec_dir":"","addresses":[],"link_routes":[],"resolvers":[],"search_domains":[]}`
	if _, err := Parse([]byte(doc)); err == nil {
		t.Fatal("want error for an empty exec_dir")
	}
}

func TestParseRejectsInvalidPrefix(t *testing.T) {
	doc := `{"version":"1","hostname":"gw","uname_m":"x86_64","exec_dir":"/tmp","addresses":[],"link_routes":["not-a-cidr"],"resolvers":[],"search_domains":[]}`
	if _, err := Parse([]byte(doc)); err == nil {
		t.Fatal("want error for an invalid link route")
	}
}

func TestParseRejectsInvalidResolver(t *testing.T) {
	doc := `{"version":"1","hostname":"gw","uname_m":"x86_64","exec_dir":"/tmp","addresses":[],"link_routes":[],"resolvers":["not-an-ip"],"search_domains":[]}`
	if _, err := Parse([]byte(doc)); err == nil {
		t.Fatal("want error for an invalid resolver")
	}
}

func TestParseRejectsMultipleDocuments(t *testing.T) {
	doc := `{"version":"1","hostname":"gw","uname_m":"x86_64","exec_dir":"/tmp","addresses":[],"link_routes":[],"resolvers":[],"search_domains":[]}` +
		`{"version":"1","hostname":"gw2","uname_m":"x86_64","exec_dir":"/tmp","addresses":[],"link_routes":[],"resolvers":[],"search_domains":[]}`
	if _, err := Parse([]byte(doc)); err == nil {
		t.Fatal("want error for more than one JSON document")
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	doc := `{"version":"1","hostname":"gw","uname_m":"x86_64","exec_dir":"/tmp","addresses":[],"link_routes":[],"resolvers":[],"search_domains":[],"bogus":1}`
	if _, err := Parse([]byte(doc)); err == nil {
		t.Fatal("want error for an unknown field")
	}
}

func TestParseRejectsInvalidManifestBase64(t *testing.T) {
	doc := `{"version":"1","hostname":"gw","uname_m":"x86_64","exec_dir":"/tmp","manifest":"not-base64!!","addresses":[],"link_routes":[],"resolvers":[],"search_domains":[]}`
	if _, err := Parse([]byte(doc)); err == nil {
		t.Fatal("want error for invalid manifest base64")
	}
}

func TestRunWrapsExecError(t *testing.T) {
	_, err := Run(func(string) ([]byte, error) {
		return nil, errors.New("boom")
	})
	if err == nil {
		t.Fatal("want an error when exec fails")
	}
}

func TestFetchManifest(t *testing.T) {
	path, content, err := FetchManifest(func(string) ([]byte, error) {
		return []byte("/etc/tj/manifest.yaml\nversion: 1\n"), nil
	})
	if err != nil {
		t.Fatalf("FetchManifest: %v", err)
	}
	if path != "/etc/tj/manifest.yaml" || string(content) != "version: 1\n" {
		t.Fatalf("FetchManifest = %q, %q, want %q, %q", path, content, "/etc/tj/manifest.yaml", "version: 1\n")
	}
}

func TestFetchManifestNoManifest(t *testing.T) {
	path, content, err := FetchManifest(func(string) ([]byte, error) {
		return []byte("\n"), nil
	})
	if err != nil {
		t.Fatalf("FetchManifest: %v", err)
	}
	if path != "" || content != nil {
		t.Fatalf("FetchManifest = %q, %v, want empty path and nil content", path, content)
	}
}

func TestFetchManifestWrapsRunError(t *testing.T) {
	_, _, err := FetchManifest(func(string) ([]byte, error) {
		return nil, errors.New("boom")
	})
	if err == nil {
		t.Fatal("want an error when run fails")
	}
}
