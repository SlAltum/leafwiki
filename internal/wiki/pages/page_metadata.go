package pages

import (
	"strings"

	"github.com/perber/wiki/internal/core/markdown"
	"github.com/perber/wiki/internal/core/tree"
)

// readPageMetadata reads the page's frontmatter and returns its tags and
// custom string properties. Both are always non-nil so the API serializes
// them as [] and {} for pages without frontmatter; read or parse failures
// yield the empty values as well.
func readPageMetadata(t *tree.TreeService, pageID string) ([]string, []string, map[string]string) {
	tags := []string{}
	aliases := []string{}
	properties := map[string]string{}

	raw, err := t.ReadPageRaw(pageID)
	if err != nil {
		return tags, aliases, properties
	}

	fm, _, has, err := markdown.ParseFrontmatter(raw)
	if err != nil || !has {
		return tags, aliases, properties
	}

	aliases = normalizeAliasInputs(fm.LeafWikiAliases)
	tags, properties = extractPageMetadata(fm.ExtraFields)
	return tags, aliases, properties
}

// normalizeAliasInputs trims aliases, drops the empty ones, removes duplicates
// (aliases are matched case-insensitively) and refuses entries long enough to
// be a paragraph rather than a name.
func normalizeAliasInputs(aliases []string) []string {
	const maxAliasRunes = 64
	seen := make(map[string]struct{}, len(aliases))
	out := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		trimmed := strings.TrimSpace(alias)
		if trimmed == "" || len([]rune(trimmed)) > maxAliasRunes {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func extractPageMetadata(fields map[string]interface{}) ([]string, map[string]string) {
	tags := []string{}
	properties := map[string]string{}

	for rawKey, value := range fields {
		key := strings.TrimSpace(rawKey)
		lower := strings.ToLower(key)

		if lower == "tags" {
			tags = normalizeMetadataTags(value)
			continue
		}
		if markdown.IsSystemKey(key) {
			continue
		}

		flattenMetadataEntry(key, value, properties)
	}

	return tags, properties
}

// NormalizeTags and NormalizeAliases are the normalization every writer applies,
// so a file on disk always matches what the readers return. A caller that writes
// without them (the agent channel does) makes "the same content again" look like
// a change.
func NormalizeTags(tags []string) []string       { return normalizeTagInputs(tags) }
func NormalizeAliases(aliases []string) []string { return normalizeAliasInputs(aliases) }

// flattenMetadataEntry recursively flattens nested YAML maps into dot-notation
// keys (e.g. {"a": {"b": "v"}} → properties["a.b"] = "v").
func flattenMetadataEntry(prefix string, value interface{}, properties map[string]string) {
	flattenMetadataEntryDepth(prefix, value, properties, 0)
}

func flattenMetadataEntryDepth(prefix string, value interface{}, properties map[string]string, depth int) {
	if depth >= maxFlattenDepth {
		return
	}
	switch v := value.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s != "" && !strings.ContainsRune(s, '\n') {
			if _, exists := properties[prefix]; !exists {
				properties[prefix] = s
			}
		}
	case map[string]interface{}:
		for childKey, childValue := range v {
			childKey = strings.TrimSpace(childKey)
			if childKey == "" {
				continue
			}
			if strings.HasPrefix(strings.ToLower(childKey), "leafwiki_") {
				continue
			}
			flattenMetadataEntryDepth(prefix+"."+childKey, childValue, properties, depth+1)
		}
	}
}

const maxFlattenDepth = 20

func normalizeMetadataTags(value interface{}) []string {
	list, ok := value.([]interface{})
	if !ok {
		return []string{}
	}

	rawTags := make([]string, 0, len(list))
	for _, item := range list {
		tag, ok := item.(string)
		if !ok {
			continue
		}
		rawTags = append(rawTags, tag)
	}

	return normalizeTagInputs(rawTags)
}
