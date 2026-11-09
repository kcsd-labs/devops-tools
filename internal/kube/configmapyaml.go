package kube

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// RenderConfigMapData writes a ConfigMap's values as the data: block of its
// manifest: keys in order, every value a string, a value of several lines as a
// literal block. What `kubectl get cm -o yaml` shows, without the metadata
// nobody edits.
func RenderConfigMapData(data map[string]string) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	values := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range keys {
		v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: data[k]}
		if strings.Contains(data[k], "\n") {
			v.Style = yaml.LiteralStyle
		}
		values.Content = append(values.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, v)
	}
	if len(keys) == 0 {
		values.Style = yaml.FlowStyle // data: {}
	}
	doc := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Value: "data"}, values,
	}}

	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	_ = enc.Encode(doc)
	_ = enc.Close()
	return b.String()
}

// ParseConfigMapData reads a data: block back. It also takes a whole manifest,
// as pasted from kubectl, and reads only its data: the name and the metadata
// are not changed from here, and saying so is better than ignoring them
// silently — so anything else that would be a change is refused.
//
// A value written as a number or a boolean is taken as the text it was
// written as: `RETRIES: 3` means "3", which is what anyone writing it meant,
// though Kubernetes itself would refuse it.
func ParseConfigMapData(text string) (map[string]string, error) {
	var doc map[string]yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, fmt.Errorf("the YAML does not parse: %w", err)
	}
	for k := range doc {
		switch k {
		case "data", "apiVersion", "kind", "metadata":
		case "binaryData":
			return nil, errors.New("binary values cannot be changed here")
		default:
			return nil, fmt.Errorf("only data can be changed here, not %s", k)
		}
	}
	node, ok := doc["data"]
	if !ok || node.Kind == 0 || (node.Kind == yaml.ScalarNode && node.Tag == "!!null") {
		return map[string]string{}, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, errors.New("data has to be a list of keys and their values")
	}
	out := make(map[string]string, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		k, v := node.Content[i].Value, node.Content[i+1]
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("the key %s appears twice", k)
		}
		switch {
		case v.Kind == yaml.ScalarNode && v.Tag == "!!null":
			out[k] = ""
		case v.Kind == yaml.ScalarNode:
			out[k] = v.Value
		default:
			return nil, fmt.Errorf("the value of %s is not text — write a file under it as a block starting with |", k)
		}
	}
	return out, nil
}
