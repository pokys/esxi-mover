// Package vmdk reads text descriptors only. It never edits a CID or parent chain.
package vmdk

import (
	"bufio"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

type Descriptor struct {
	CID, ParentCID, ParentHint, CreateType string
	Extents                                []Extent
	Thin                                   bool
	Bytes                                  int64
	Fields                                 map[string]string
}
type Extent struct {
	Sectors    int64
	Kind, File string
}

var extentPattern = regexp.MustCompile(`^RW\s+(\d+)\s+([A-Za-z0-9]+)\s+"([^"\r\n]+)"(?:\s+0)?$`)
var cidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}$`)
var fieldPattern = regexp.MustCompile(`^[a-zA-Z0-9_.]+$`)

func Parse(text string) (Descriptor, error) {
	d := Descriptor{Fields: map[string]string{}}
	if len(text) > 1<<20 || strings.ContainsRune(text, 0) {
		return d, fmt.Errorf("binary or oversized descriptor")
	}
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := extentPattern.FindStringSubmatch(line); m != nil {
			n, err := strconv.ParseInt(m[1], 10, 64)
			if err != nil || n <= 0 || n > math.MaxInt64/512-d.Bytes/512 {
				return d, fmt.Errorf("invalid virtual size")
			}
			d.Extents = append(d.Extents, Extent{n, m[2], m[3]})
			d.Bytes += n * 512
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if !ok || !fieldPattern.MatchString(k) || v == "" {
			return d, fmt.Errorf("unrecognized descriptor syntax")
		}
		if strings.HasPrefix(v, `"`) {
			if len(v) < 2 || !strings.HasSuffix(v, `"`) {
				return d, fmt.Errorf("invalid quoted descriptor value")
			}
			v = v[1 : len(v)-1]
			if strings.Contains(v, `"`) {
				return d, fmt.Errorf("ambiguous descriptor value")
			}
		}
		if _, ok := d.Fields[k]; ok {
			return d, fmt.Errorf("duplicate descriptor field")
		}
		d.Fields[k] = v
	}
	if scanner.Err() != nil {
		return d, scanner.Err()
	}
	d.CID = d.Fields["cid"]
	d.ParentCID = d.Fields["parentcid"]
	d.ParentHint = d.Fields["parentfilenamehint"]
	d.CreateType = d.Fields["createtype"]
	d.Thin = d.Fields["ddb.thinprovisioned"] == "1"
	// ESXi writes version 3 for a disk with change block tracking enabled, and
	// the version says nothing about the fields this package relies on.
	switch d.Fields["version"] {
	case "1", "2", "3":
	default:
		return d, fmt.Errorf("unsupported descriptor version %q", d.Fields["version"])
	}
	if !cidPattern.MatchString(d.CID) || !cidPattern.MatchString(d.ParentCID) || len(d.Extents) == 0 || d.CreateType == "" {
		return d, fmt.Errorf("incomplete or invalid descriptor")
	}
	return d, nil
}
func (d Descriptor) Standalone() error {
	if !strings.EqualFold(d.ParentCID, "ffffffff") || d.ParentHint != "" {
		return fmt.Errorf("snapshot/linked-clone parent chain")
	}
	if d.CreateType != "vmfs" {
		return fmt.Errorf("unsupported disk createType %s (snapshot, RDM or non-VMFS disk)", d.CreateType)
	}
	if len(d.Extents) != 1 || d.Extents[0].Kind != "VMFS" {
		return fmt.Errorf("only a single standard VMFS extent is supported")
	}
	for k := range d.Fields {
		if strings.Contains(k, "encrypt") || strings.Contains(k, "keyid") {
			return fmt.Errorf("encrypted descriptor")
		}
	}
	return nil
}
