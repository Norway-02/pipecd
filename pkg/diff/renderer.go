// Copyright 2024 The PipeCD Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package diff

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"
)

type Renderer struct {
	leftPadding    int
	maskPathPrefix string
}

type RenderOption func(*Renderer)

const maskString = "*****"

func WithLeftPadding(p int) RenderOption {
	return func(r *Renderer) {
		r.leftPadding = p
	}
}

func WithMaskPath(prefix string) RenderOption {
	return func(r *Renderer) {
		r.maskPathPrefix = prefix
	}
}

func NewRenderer(opts ...RenderOption) *Renderer {
	r := &Renderer{}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *Renderer) Render(ns Nodes) string {
	if len(ns) == 0 {
		return ""
	}
	if r == nil {
		r = &Renderer{}
	}

	var prePath []PathStep
	var b strings.Builder

	printValue := func(mark string, v reflect.Value, lastStep PathStep, depth int) {
		if !v.IsValid() {
			return
		}

		nodeString, nl := renderNodeValue(v, "")
		if lastStep.Type == SliceIndexPathStep {
			nl = false
		}

		pad := depth*2 - 1
		if pad < 0 {
			pad = 0
		}

		switch {
		case lastStep.Type == SliceIndexPathStep:
			fmt.Fprintf(&b, "%s%*s- ", mark, pad, "")
		case nl:
			if lastStep.String() == "" {
				if pad > 0 {
					fmt.Fprintf(&b, "%s%*s\n", mark, pad, "")
				} else {
					fmt.Fprintf(&b, "%s\n", mark)
				}
			} else {
				fmt.Fprintf(&b, "%s%*s%s:\n", mark, pad, "", lastStep.String())
			}
		default:
			if lastStep.String() == "" {
				if pad > 0 {
					fmt.Fprintf(&b, "%s%*s ", mark, pad, "")
				} else {
					fmt.Fprintf(&b, "%s ", mark)
				}
			} else {
				fmt.Fprintf(&b, "%s%*s%s: ", mark, pad, "", lastStep.String())
			}
		}

		parts := strings.Split(nodeString, "\n")
		for i, p := range parts {
			if lastStep.Type != SliceIndexPathStep {
				if nl {
					fmt.Fprintf(&b, "%s%*s%s\n", mark, depth*2+1, "", p)
				} else {
					fmt.Fprintf(&b, "%s\n", p)
				}
				continue
			}
			if i == 0 {
				fmt.Fprintf(&b, "%s\n", p)
				continue
			}
			fmt.Fprintf(&b, "%s%*s%s\n", mark, depth*2+1, "", p)
		}
	}

	for _, n := range ns {
		pathLen := len(n.Path)
		if pathLen == 0 {
			valueX, valueY := n.ValueX, n.ValueY
			if r.maskPathPrefix != "" && strings.HasPrefix(n.PathString, r.maskPathPrefix) {
				valueX = reflect.ValueOf(maskString)
				valueY = reflect.ValueOf(maskString)
			}

			fmt.Fprintf(&b, "%*s#%s\n", r.leftPadding*2, "", n.PathString)
			printValue("-", valueX, PathStep{}, r.leftPadding)
			printValue("+", valueY, PathStep{}, r.leftPadding)
			b.WriteString("\n")
			continue
		}

		duplicateDepth := pathDuplicateDepth(n.Path, prePath)
		prePath = n.Path

		var array bool
		for i := duplicateDepth; i < pathLen-1; i++ {
			if n.Path[i].Type == SliceIndexPathStep {
				fmt.Fprintf(&b, "%*s-", (r.leftPadding+i)*2, "")
				array = true
				continue
			}
			if array {
				fmt.Fprintf(&b, " %s:\n", n.Path[i].String())
				array = false
				continue
			}
			fmt.Fprintf(&b, "%*s%s:\n", (r.leftPadding+i)*2, "", n.Path[i].String())
		}
		if array {
			b.WriteString("\n")
		}

		lastStep := n.Path[pathLen-1]
		valueX, valueY := n.ValueX, n.ValueY
		if r.maskPathPrefix != "" && strings.HasPrefix(n.PathString, r.maskPathPrefix) {
			valueX = reflect.ValueOf(maskString)
			valueY = reflect.ValueOf(maskString)
		}

		fmt.Fprintf(&b, "%*s#%s\n", (r.leftPadding+pathLen-1)*2, "", n.PathString)
		printValue("-", valueX, lastStep, r.leftPadding+pathLen-1)
		printValue("+", valueY, lastStep, r.leftPadding+pathLen-1)
		b.WriteString("\n")
	}

	return b.String()
}

func pathDuplicateDepth(x, y []PathStep) int {
	minLen := min(len(x), len(y))

	for i := 0; i < minLen; i++ {
		if x[i] == y[i] {
			continue
		}
		return i
	}
	return 0
}

func renderNodeValue(v reflect.Value, prefix string) (string, bool) {
	if !v.IsValid() {
		return "", false
	}

	switch v.Kind() {
	case reflect.Map:
		out := make([]string, 0, v.Len())
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool {
			return keys[i].String() < keys[j].String()
		})
		for _, k := range keys {
			sub := v.MapIndex(k)
			subString, nl := renderNodeValue(sub, prefix+"  ")
			if !nl {
				out = append(out, fmt.Sprintf("%s%s: %s", prefix, k.String(), subString))
				continue
			}
			out = append(out, fmt.Sprintf("%s%s:\n%s", prefix, k.String(), subString))
		}
		if len(out) == 0 {
			return "", false
		}
		return strings.Join(out, "\n"), true

	case reflect.Slice, reflect.Array:
		out := make([]string, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			sub, _ := renderNodeValue(v.Index(i), prefix+"  ")
			parts := strings.Split(sub, "\n")
			for i, p := range parts {
				p = strings.TrimPrefix(p, prefix+"  ")
				if i == 0 {
					out = append(out, fmt.Sprintf("%s- %s", prefix, p))
					continue
				}
				out = append(out, fmt.Sprintf("%s  %s", prefix, p))
			}
		}
		return strings.Join(out, "\n"), true

	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return "", false
		}
		return renderNodeValue(v.Elem(), prefix)

	case reflect.String:
		return v.String(), false

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), false

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10), false

	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'f', -1, 64), false

	case reflect.Bool:
		return strconv.FormatBool(v.Bool()), false

	default:
		return v.String(), false
	}
}

func RenderPrimitiveValue(v reflect.Value) string {
	if !v.IsValid() {
		return ""
	}

	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return ""
		}
		return RenderPrimitiveValue(v.Elem())

	case reflect.String:
		return v.String()

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10)

	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'f', -1, 64)

	case reflect.Bool:
		return strconv.FormatBool(v.Bool())

	default:
		return v.String()
	}
}

// RenderByCommand converts the given objects into yaml and then runs the command to compare them.
func RenderByCommand(command string, old, new any) ([]byte, error) {
	_, err := exec.LookPath(command)
	if err != nil {
		return nil, fmt.Errorf("failed to find the command '%s', err = %w", command, err)
	}

	oldBytes, err := yaml.Marshal(old)
	if err != nil {
		return nil, err
	}
	newBytes, err := yaml.Marshal(new)
	if err != nil {
		return nil, err
	}

	oldFile, err := os.CreateTemp("", "old")
	if err != nil {
		return nil, err
	}
	defer os.Remove(oldFile.Name())
	if _, err := oldFile.Write(oldBytes); err != nil {
		oldFile.Close()
		return nil, err
	}
	if err := oldFile.Close(); err != nil {
		return nil, err
	}

	newFile, err := os.CreateTemp("", "new")
	if err != nil {
		return nil, err
	}
	defer os.Remove(newFile.Name())
	if _, err := newFile.Write(newBytes); err != nil {
		newFile.Close()
		return nil, err
	}
	if err := newFile.Close(); err != nil {
		return nil, err
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(command, "-u", "-N", oldFile.Name(), newFile.Name())
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if stdout.Len() > 0 {
		// diff exits with a non-zero status when the files don't match.
		// Ignore that failure as long as we get output.
		err = nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to run diff, err = %w, %s", err, stderr.String())
	}

	// Remove two-line header from output.
	data := bytes.TrimSpace(stdout.Bytes())
	rows := bytes.SplitN(data, []byte("\n"), 3)
	if len(rows) == 3 {
		return rows[2], nil
	}
	return data, nil
}
