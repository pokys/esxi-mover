package vmx

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// ValidateVMXF accepts standalone metadata and blocks external or team
// backings. A real host stores a VMware Tools manifest here, with dozens of
// element and attribute names no whitelist can predict, so the shape of the
// document is not the signal: a value naming another file or datastore is.
// The VMX basename does not change during a datastore move, so a relative
// vmxPathName needs no rewrite.
func ValidateVMXF(raw, vmxName string) error {
	external := func(value string) bool {
		low := strings.ToLower(value)
		return strings.ContainsAny(value, "/\\") || strings.HasPrefix(value, "[") ||
			strings.Contains(low, ".vmdk") || strings.Contains(low, ".vmx")
	}
	d := xml.NewDecoder(strings.NewReader(raw))
	depth, vmCount, pathCount := 0, 0, 0
	root, hasVMXPath := false, false
	stack := []string{}
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
			if v.Name.Space != "" {
				return fmt.Errorf("namespaced VMXF element is unsupported")
			}
			if depth == 0 {
				if root || v.Name.Local != "Foundry" {
					return fmt.Errorf("unsupported VMXF root")
				}
				root = true
			}
			switch v.Name.Local {
			case "VM":
				vmCount++
			case "vmxPathName":
				pathCount++
			}
			for _, attr := range v.Attr {
				if attr.Name.Space != "" {
					return fmt.Errorf("namespaced VMXF attribute is unsupported")
				}
				if external(attr.Value) {
					return fmt.Errorf("external VMXF dependency")
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
			if depth > 0 && stack[depth-1] == "vmxPathName" {
				if value != vmxName {
					return fmt.Errorf("VMXF points to a different VMX")
				}
				hasVMXPath = true
				continue
			}
			if external(value) {
				return fmt.Errorf("external VMXF dependency")
			}
		case xml.ProcInst:
			if v.Target != "xml" {
				return fmt.Errorf("VMXF processing instructions are unsupported")
			}
		case xml.Directive:
			return fmt.Errorf("VMXF directives are unsupported")
		}
	}
	// Exactly one VM rules out a team file. A vmxPathName is optional, but when
	// present it must name this VM's own configuration.
	if !root || depth != 0 || vmCount != 1 || pathCount > 1 || (pathCount == 1 && !hasVMXPath) {
		return fmt.Errorf("unknown or non-standalone VMXF structure")
	}
	return nil
}
