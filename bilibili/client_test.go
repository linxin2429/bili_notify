package bilibili

import (
	"encoding/json"
	"testing"
)

func TestParseDynamic(t *testing.T) {
	raw := json.RawMessage(`{
		"id_str":"12345",
		"type":"DYNAMIC_TYPE_AV",
		"modules":{
			"module_author":{"name":"tester","pub_ts":1700000000},
			"module_dynamic":{"desc":{"text":"new video"},"major":{"archive":{"title":"title","desc":"description"}}}
		}
	}`)
	got, name, err := parseDynamic("42", raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "12345" || name != "tester" || got.Summary != "new video\ntitle\ndescription" {
		t.Fatalf("parseDynamic() = %#v, %q", got, name)
	}
}

func TestParseDynamicRejectsUnknownType(t *testing.T) {
	raw := json.RawMessage(`{"id_str":"1","type":"NEW_TYPE","modules":{"module_author":{"pub_ts":1}}}`)
	if _, _, err := parseDynamic("42", raw); err == nil {
		t.Fatal("parseDynamic() accepted unknown type")
	}
}
