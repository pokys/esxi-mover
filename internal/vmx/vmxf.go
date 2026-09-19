package vmx

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// ValidateVMXF accepts standalone metadata without external/team backings.
// The VMX basename does not change during a datastore move, so no rewrite is
// needed for a relative vmxPathName. Absolute paths and unknown dependencies block.
func ValidateVMXF(raw, vmxName string) error {
	d := xml.NewDecoder(strings.NewReader(raw))
	depth := 0
	root := false
	stack := []string{}
	parents := map[string]string{"Foundry": "", "VM": "Foundry", "VMId": "VM", "ClientMetaData": "VM", "clientMetaDataAttributes": "ClientMetaData", "HistoryEventList": "ClientMetaData", "vmxPathName": "VM"}
	vmCount, pathCount := 0, 0
	hasVMXPath := false
	for {
		t, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return fmt.Errorf("invalid VMXF XML")
		}
		switch v := t.(type) {
		case xml.StartElement:
			parent, known := parents[v.Name.Local]
			actualParent := ""
			if depth > 0 {
				actualParent = stack[depth-1]
			}
			if !known || v.Name.Space != "" || parent != actualParent {
				return fmt.Errorf("unknown or non-standalone VMXF structure")
			}
			if v.Name.Local == "VM" {
				vmCount++
			}
			if v.Name.Local == "vmxPathName" {
				pathCount++
			}
			if depth == 0 {
				if root || v.Name.Local != "Foundry" {
					return fmt.Errorf("unsupported VMXF root")
				}
				root = true
			}
			for _, attr := range v.Attr {
				if attr.Name.Space != "" || attr.Name.Local != "type" || attr.Value != "string" {
					return fmt.Errorf("unknown VMXF attribute")
				}
			}
			stack = append(stack, v.Name.Local)
			depth++
		case xml.EndElement:
			depth--
			if depth < 0 {
				return fmt.Errorf("invalid VMXF nesting")
			}
			stack = stack[:depth]
		case xml.CharData:
			value := strings.TrimSpace(string(v))
			if value == "" {
				continue
			}
			if strings.ContainsAny(value, "/\\") || strings.HasPrefix(value, "[") || strings.Contains(strings.ToLower(value), ".vmdk") {
				return fmt.Errorf("external VMXF dependency")
			}
			if depth > 0 && stack[depth-1] == "vmxPathName" && value != vmxName {
				return fmt.Errorf("VMXF points to a different VMX")
			}
			if depth > 0 && stack[depth-1] == "vmxPathName" {
				hasVMXPath = true
			}
			if depth == 0 || (stack[depth-1] != "VMId" && stack[depth-1] != "vmxPathName") {
				return fmt.Errorf("unknown VMXF metadata value")
			}
		case xml.ProcInst:
			if v.Target != "xml" {
				return fmt.Errorf("VMXF processing instructions are unsupported")
			}
		case xml.Directive:
			return fmt.Errorf("VMXF directives are unsupported")
		}
	}
	if !root || depth != 0 || vmCount != 1 || pathCount != 1 || !hasVMXPath {
		return fmt.Errorf("incomplete VMXF")
	}
	return nil
}
