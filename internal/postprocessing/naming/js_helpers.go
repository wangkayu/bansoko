package naming

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dop251/goja"
)

const helperOutputLimit = MaxOutputBytes

// chainValuable reports whether a chain part carries any of its value beyond
// the separator prefix it was written with: a part that trims to empty, or
// that holds no letters or digits at all, is its prefix left alone by an
// empty value (' " ", "." from '.' + (x ?? ”)) and the chain drops it with
// the prefix. A real value — latin, CJK, a number — keeps the part verbatim.
func chainValuable(part string) bool {
	values := false
	for _, r := range part {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			values = true
			break
		}
	}
	return values
}

func jsString(v goja.Value) (string, error) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return "", fmt.Errorf("naming template value is missing; choose an explicit fallback")
	}
	if object, ok := v.(*goja.Object); ok {
		if object.ClassName() == "RegExp" {
			pattern := object.Get("source")
			if pattern == nil || goja.IsUndefined(pattern) {
				return "", nil
			}
			return jsString(pattern)
		}
		return "", fmt.Errorf("naming template value must be a primitive string, number, or boolean")
	}
	if stringValue, ok := v.(goja.String); ok && stringValue.Length() > helperOutputLimit {
		return "", fmt.Errorf("naming template string exceeds the %d byte limit", helperOutputLimit)
	}
	text := v.String()
	if len([]byte(text)) > helperOutputLimit {
		return "", fmt.Errorf("naming template string exceeds the %d byte limit", helperOutputLimit)
	}
	return text, nil
}

func boundedStringResult(value string) (string, error) {
	if len([]byte(value)) > helperOutputLimit {
		return "", fmt.Errorf("naming template output exceeds the %d byte limit", helperOutputLimit)
	}
	return value, nil
}

type helperDefinition struct {
	CatalogFunction
	register func() interface{}
	// deprecated helpers still compile and render (stored templates and
	// frozen snapshots use them) but are not advertised in the catalog.
	deprecated bool
}

var helperDefinitions = []helperDefinition{
	{CatalogFunction: CatalogFunction{Name: "sanitize", Arguments: []string{"value"}, Signature: "sanitize(value) string", Description: "Replaces filesystem-unsafe and control characters with underscores.", Example: "${sanitize(var1)}"}, register: func() interface{} {
		return func(v goja.Value) (string, error) {
			value, err := jsString(v)
			if err != nil {
				return "", err
			}
			return boundedStringResult(sanitize(value))
		}
	}},
	{CatalogFunction: CatalogFunction{Name: "pad", Arguments: []string{"value", "width"}, Signature: "pad(value, width) string", Description: "Zero-pads the value on the left up to width.", Example: "${pad(var1, 2)}"}, register: func() interface{} {
		return func(v goja.Value, width int) (string, error) {
			value, err := jsString(v)
			if err != nil {
				return "", err
			}
			return jsPadWith(value, width, '0', true)
		}
	}},
	{CatalogFunction: CatalogFunction{Name: "contains", Arguments: []string{"value", "needle"}, Signature: "contains(value, needle) bool", Description: "Reports whether value contains needle.", Example: "${contains(var1, var2)}"}, register: func() interface{} {
		return func(v, needle goja.Value) (bool, error) {
			value, err := jsString(v)
			if err != nil {
				return false, err
			}
			n, err := jsString(needle)
			return strings.Contains(value, n), err
		}
	}},
	{CatalogFunction: CatalogFunction{Name: "hasPrefix", Arguments: []string{"value", "prefix"}, Signature: "hasPrefix(value, prefix) bool", Description: "Reports whether value starts with prefix.", Example: "${hasPrefix(var1, var2)}"}, register: func() interface{} {
		return func(v, prefix goja.Value) (bool, error) {
			value, err := jsString(v)
			if err != nil {
				return false, err
			}
			p, err := jsString(prefix)
			return strings.HasPrefix(value, p), err
		}
	}},
	{CatalogFunction: CatalogFunction{Name: "hasSuffix", Arguments: []string{"value", "suffix"}, Signature: "hasSuffix(value, suffix) bool", Description: "Reports whether value ends with suffix.", Example: "${hasSuffix(var1, var2)}"}, register: func() interface{} {
		return func(v, suffix goja.Value) (bool, error) {
			value, err := jsString(v)
			if err != nil {
				return false, err
			}
			s, err := jsString(suffix)
			return strings.HasSuffix(value, s), err
		}
	}},
	{CatalogFunction: CatalogFunction{Name: "split", Arguments: []string{"value", "sep"}, Signature: "split(value, sep) list", Description: "Splits value into a bounded list.", Example: "${join(split(var1, var2), var2)}"}, register: func() interface{} {
		return func(v, sep goja.Value) ([]string, error) {
			value, err := jsString(v)
			if err != nil {
				return nil, err
			}
			separator, err := jsString(sep)
			if err != nil {
				return nil, err
			}
			parts := strings.Split(value, separator)
			if len(parts) > MaxCollectionLen {
				return nil, fmt.Errorf("naming template collection exceeds the %d item limit", MaxCollectionLen)
			}
			return parts, nil
		}
	}},
	{CatalogFunction: CatalogFunction{Name: "padLeft", Arguments: []string{"value", "width", "fill"}, Signature: "padLeft(value, width, fill) string", Description: "Pads the value on the left with the first character of fill.", Example: "${padLeft(var1, 4, var2)}"}},
	{CatalogFunction: CatalogFunction{Name: "padRight", Arguments: []string{"value", "width", "fill"}, Signature: "padRight(value, width, fill) string", Description: "Pads the value on the right with the first character of fill.", Example: "${padRight(var1, 4, var2)}"}},
	{CatalogFunction: CatalogFunction{Name: "replace", Arguments: []string{"value", "search", "replacement"}, Signature: "replace(value, search, replacement) string", Description: "Replaces all literal occurrences of search in value.", Example: "${replace(var1, var2, var3)}"}},
	{CatalogFunction: CatalogFunction{Name: "replaceRegex", Arguments: []string{"value", "pattern", "replacement"}, Signature: "replaceRegex(value, pattern, replacement) string", Description: "Replaces every regex match; patterns are bounded to 512 bytes.", Example: "${replaceRegex(var1, var2, var3)}"}},
	{CatalogFunction: CatalogFunction{Name: "extract", Arguments: []string{"value", "pattern", "captureIndex?"}, Signature: "extract(value, pattern[, captureIndex]) string|null", Description: "Returns a capture group, or null when no capture matches; invalid calls fail. Choose an explicit fallback for missing values.", Example: "${extract(var1, var2)}"}},
	{CatalogFunction: CatalogFunction{Name: "match", Arguments: []string{"value", "pattern"}, Signature: "match(value, pattern) bool", Description: "Reports whether the regex matches the value.", Example: "${match(var1, var2)}"}},
	{CatalogFunction: CatalogFunction{Name: "upper", Arguments: []string{"value"}, Signature: "upper(value) string", Description: "Uppercases the value.", Example: "${upper(var1)}"}},
	{CatalogFunction: CatalogFunction{Name: "lower", Arguments: []string{"value"}, Signature: "lower(value) string", Description: "Lowercases the value.", Example: "${lower(var1)}"}},
	{CatalogFunction: CatalogFunction{Name: "trim", Arguments: []string{"value"}, Signature: "trim(value) string", Description: "Trims surrounding whitespace.", Example: "${trim(var1)}"}},
	{CatalogFunction: CatalogFunction{Name: "defaultIfEmpty", Arguments: []string{"value", "fallback"}, Signature: "defaultIfEmpty(value, fallback) string", Description: "Returns fallback when value is null or empty; the same function and argument order as notification templates.", Example: "${defaultIfEmpty(var1, var2)}"}},
	// defaultValue is the earlier argument order of defaultIfEmpty, kept so
	// stored templates and frozen snapshots still render.
	{CatalogFunction: CatalogFunction{Name: "defaultValue", Arguments: []string{"fallback", "value"}, Signature: "defaultValue(fallback, value) string", Description: "Deprecated: defaultIfEmpty(value, fallback).", Example: "${defaultValue('Season 01', anime.season_folder)}"}, deprecated: true},
	{CatalogFunction: CatalogFunction{Name: "coalesce", Arguments: []string{"value..."}, Signature: "coalesce(...values) string|null", Description: "Returns the first present, non-empty argument, or null when none exists.", Example: "${coalesce(var1, var2, var3)}"}},
	{CatalogFunction: CatalogFunction{Name: "join", Arguments: []string{"list", "sep"}, Signature: "join(list, sep) string", Description: "Joins a list with the separator.", Example: "${join(var1, var2)}"}},
	// chain is the variadic tech-chain composer: every part is an ordinary
	// expression that carries its own separator prefix (' ' + (ep.s00e00 ?? ''),
	// '.' + (media.resolution ?? '')), a part whose result is only its
	// separator prefix — no letters or digits — is dropped whole, prefix
	// included, and the kept parts concatenate verbatim; all parts dropped
	// render "" and the caller owns the surrounding literals (the extension
	// dot belongs in the chain as '.' + file.ext). Guard each fact read
	// inside a part with ?? '': a fact that carries no value for this render
	// reads null, and the language's undefined-variable check does not reach
	// chain arguments. The catalog example is a var1/var2 placeholder frame —
	// examples teach the shape and the user substitutes their own facts; the
	// catalog never presets a fact pick.
	{CatalogFunction: CatalogFunction{Name: "chain", Arguments: []string{"part..."}, Signature: "chain(...parts) string", Description: "Concatenates the parts, dropping a part that carries no letters or digits (its separator prefix left alone) whole with its prefix; all parts dropped is an empty string. Guard fact reads with ?? '' inside a part: a fact with no value reads null.", Example: `${chain(' ' + (var1 ?? ''), '.' + (var2 ?? ''))}`}, register: func() interface{} {
		return func(values ...goja.Value) (string, error) {
			var chain strings.Builder
			bytes := 0
			for _, v := range values {
				if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
					continue
				}
				part, err := jsString(v)
				if err != nil {
					return "", err
				}
				if !chainValuable(part) {
					continue
				}
				if bytes+len(part) > helperOutputLimit {
					return "", fmt.Errorf("naming template output exceeds the %d byte limit", helperOutputLimit)
				}
				chain.WriteString(part)
				bytes += len(part)
			}
			return boundedStringResult(chain.String())
		}
	}},
	{CatalogFunction: CatalogFunction{Name: "toInt", Arguments: []string{"value"}, Signature: "toInt(value) number", Description: "Parses the value as an integer; returns 0 on failure.", Example: "${toInt(var1)}"}},
	// seasonEpisode is the runtime of the ${s00e00} shorthand expansion: the
	// ep.s00e00 composite semantics (SeasonEpisode, season.go) called with the
	// guard-preprocessed arguments the expansion text builds. It is deprecated
	// so the catalog keeps advertising the shorthand instead of the helper.
	{CatalogFunction: CatalogFunction{Name: "seasonEpisode", Arguments: []string{"kind", "season", "number", "numberEnd"}, Signature: "seasonEpisode(kind, season, number, numberEnd) string", Description: "Deprecated: the ${s00e00} shorthand; composes the season/episode pair.", Example: "${s00e00}"}, deprecated: true, register: func() interface{} {
		return func(kind, season, number, numberEnd goja.Value) (string, error) {
			kindText, err := jsString(kind)
			if err != nil {
				return "", err
			}
			seasonText, err := jsString(season)
			if err != nil {
				return "", err
			}
			numberText, err := jsString(number)
			if err != nil {
				return "", err
			}
			endText, err := jsString(numberEnd)
			if err != nil {
				return "", err
			}
			var seasonValue *int64
			if seasonText != "" {
				parsed, parseErr := strconv.ParseInt(strings.TrimSpace(seasonText), 10, 64)
				if parseErr != nil {
					return "", fmt.Errorf("naming shorthand s00e00 needs a numeric season")
				}
				seasonValue = &parsed
			}
			return boundedStringResult(SeasonEpisode(kindText, seasonValue, numberText, endText))
		}
	}},
}

func helperCatalog() []CatalogFunction {
	result := make([]CatalogFunction, 0, len(helperDefinitions))
	for _, definition := range helperDefinitions {
		if !definition.deprecated {
			result = append(result, definition.CatalogFunction)
		}
	}
	return result
}

func registeredHelpers(defaultFn interface{}) map[string]interface{} {
	functions := map[string]interface{}{
		"sanitize": func(v goja.Value) (string, error) {
			value, err := jsString(v)
			if err != nil {
				return "", err
			}
			return boundedStringResult(sanitize(value))
		},
		"pad": func(v goja.Value, width int) (string, error) {
			value, err := jsString(v)
			if err != nil {
				return "", err
			}
			return jsPadWith(value, width, '0', true)
		},
		"padLeft": func(v goja.Value, width int, fill goja.Value) (string, error) {
			value, err := jsString(v)
			if err != nil {
				return "", err
			}
			fillText, err := jsString(fill)
			if err != nil {
				return "", err
			}
			return jsPadWith(value, width, jsFillRune(fillText), true)
		},
		"padRight": func(v goja.Value, width int, fill goja.Value) (string, error) {
			value, err := jsString(v)
			if err != nil {
				return "", err
			}
			fillText, err := jsString(fill)
			if err != nil {
				return "", err
			}
			return jsPadWith(value, width, jsFillRune(fillText), false)
		},
		"replace": func(v, search, replacement goja.Value) (string, error) {
			value, err := jsString(v)
			if err != nil {
				return "", err
			}
			searchText, err := jsString(search)
			if err != nil {
				return "", err
			}
			replacementText, err := jsString(replacement)
			if err != nil {
				return "", err
			}
			if searchText == "" && len([]rune(value))+1 > helperOutputLimit {
				return "", fmt.Errorf("naming template output exceeds the %d byte limit", helperOutputLimit)
			}
			if count := strings.Count(value, searchText); count > 0 && int64(len(value))+int64(count)*(int64(len(replacementText))-int64(len(searchText))) > helperOutputLimit {
				return "", fmt.Errorf("naming template output exceeds the %d byte limit", helperOutputLimit)
			}
			return boundedStringResult(strings.ReplaceAll(value, searchText, replacementText))
		},
		"replaceRegex": func(v, pattern, replacement goja.Value) (string, error) {
			value, err := jsString(v)
			if err != nil {
				return "", err
			}
			p, err := jsString(pattern)
			if err != nil {
				return "", err
			}
			r, err := jsString(replacement)
			if err != nil {
				return "", err
			}
			re, err := compileJSRegex(p)
			if err != nil {
				return "", err
			}
			return boundedRegexReplace(re, value, r)
		},
		"extract": func(v, pattern goja.Value, index ...int) (interface{}, error) {
			value, err := jsString(v)
			if err != nil {
				return nil, err
			}
			p, err := jsString(pattern)
			if err != nil {
				return nil, err
			}
			re, err := compileJSRegex(p)
			if err != nil {
				return nil, err
			}
			i := 1
			if len(index) > 0 {
				i = index[0]
			}
			if i < 0 || i > re.NumSubexp() {
				return nil, fmt.Errorf("naming extract capture index is out of range")
			}
			indices := re.FindStringSubmatchIndex(value)
			if len(indices) == 0 || indices[2*i] < 0 {
				return nil, nil
			}
			return boundedStringResult(value[indices[2*i]:indices[2*i+1]])
		},
		"match": func(v, pattern goja.Value) (bool, error) {
			value, err := jsString(v)
			if err != nil {
				return false, err
			}
			p, err := jsString(pattern)
			if err != nil {
				return false, err
			}
			re, err := compileJSRegex(p)
			if err != nil {
				return false, err
			}
			return re.MatchString(value), nil
		},
		"upper": func(v goja.Value) (string, error) {
			value, err := jsString(v)
			if err != nil {
				return "", err
			}
			return boundedStringResult(strings.ToUpper(value))
		},
		"lower": func(v goja.Value) (string, error) {
			value, err := jsString(v)
			if err != nil {
				return "", err
			}
			return boundedStringResult(strings.ToLower(value))
		},
		"trim": func(v goja.Value) (string, error) {
			value, err := jsString(v)
			if err != nil {
				return "", err
			}
			return boundedStringResult(strings.TrimSpace(value))
		},
		"defaultValue": defaultFn,
		"defaultIfEmpty": func(value, fallback goja.Value) (string, error) {
			return defaultFn.(func(goja.Value, goja.Value) (string, error))(fallback, value)
		},
		"coalesce": func(values ...goja.Value) (interface{}, error) {
			for _, v := range values {
				if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
					continue
				}
				value, err := jsString(v)
				if err != nil {
					return "", err
				}
				if value != "" {
					return value, nil
				}
			}
			return nil, nil
		},
		"join": func(list goja.Value, sep goja.Value) (string, error) {
			if list == nil || goja.IsUndefined(list) || goja.IsNull(list) {
				return "", nil
			}
			obj := list.ToObject(nil)
			length := obj.Get("length").ToInteger()
			if length < 0 || length > MaxCollectionLen {
				return "", fmt.Errorf("naming template collection exceeds the %d item limit", MaxCollectionLen)
			}
			separator, err := jsString(sep)
			if err != nil {
				return "", err
			}
			parts := make([]string, 0, int(length))
			bytes := 0
			for i := int64(0); i < length; i++ {
				part, err := jsString(obj.Get(fmt.Sprint(i)))
				if err != nil {
					return "", err
				}
				bytes += len(part)
				if i > 0 {
					bytes += len(separator)
				}
				if bytes > helperOutputLimit {
					return "", fmt.Errorf("naming template output exceeds the %d byte limit", helperOutputLimit)
				}
				parts = append(parts, part)
			}
			return strings.Join(parts, separator), nil
		},
		"contains": func(v, needle goja.Value) (bool, error) {
			value, err := jsString(v)
			if err != nil {
				return false, err
			}
			needleText, err := jsString(needle)
			if err != nil {
				return false, err
			}
			return strings.Contains(value, needleText), nil
		},
		"hasPrefix": func(v, prefix goja.Value) (bool, error) {
			value, err := jsString(v)
			if err != nil {
				return false, err
			}
			prefixText, err := jsString(prefix)
			if err != nil {
				return false, err
			}
			return strings.HasPrefix(value, prefixText), nil
		},
		"hasSuffix": func(v, suffix goja.Value) (bool, error) {
			value, err := jsString(v)
			if err != nil {
				return false, err
			}
			suffixText, err := jsString(suffix)
			if err != nil {
				return false, err
			}
			return strings.HasSuffix(value, suffixText), nil
		},
		"split": func(v, sep goja.Value) ([]string, error) {
			value, err := jsString(v)
			if err != nil {
				return nil, err
			}
			separator, err := jsString(sep)
			if err != nil {
				return nil, err
			}
			parts := strings.Split(value, separator)
			if len(parts) > MaxCollectionLen {
				return nil, fmt.Errorf("naming template collection exceeds the %d item limit", MaxCollectionLen)
			}
			return parts, nil
		},
		"toInt": func(v goja.Value) (int, error) {
			value, err := jsString(v)
			if err != nil {
				return 0, err
			}
			n, _ := strconv.Atoi(strings.TrimSpace(value))
			return n, nil
		},
	}
	registered := make(map[string]interface{}, len(helperDefinitions))
	for _, definition := range helperDefinitions {
		fn := functions[definition.Name]
		if definition.register != nil {
			fn = definition.register()
		}
		registered[definition.Name] = fn
	}
	return registered
}

func jsHelpers() map[string]interface{} {
	defaultFn := func(fallback, value goja.Value) (string, error) {
		if value == nil || goja.IsUndefined(value) || goja.IsNull(value) {
			return jsString(fallback)
		}
		valueText, err := jsString(value)
		if err != nil {
			return "", err
		}
		if valueText == "" {
			return jsString(fallback)
		}
		return valueText, nil
	}
	return registeredHelpers(defaultFn)
}

func boundedRegexReplace(r *regexp.Regexp, value, replacement string) (string, error) {
	matches := r.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return value, nil
	}
	result := make([]byte, 0, len(value))
	last := 0
	for _, match := range matches {
		result = append(result, value[last:match[0]]...)
		expanded := r.Expand(nil, []byte(replacement), []byte(value), match)
		if len(result)+len(expanded) > helperOutputLimit {
			return "", fmt.Errorf("naming template output exceeds the %d byte limit", helperOutputLimit)
		}
		result = append(result, expanded...)
		last = match[1]
	}
	result = append(result, value[last:]...)
	if len(result) > helperOutputLimit {
		return "", fmt.Errorf("naming template output exceeds the %d byte limit", helperOutputLimit)
	}
	return string(result), nil
}

func compileJSRegex(pattern string) (*regexp.Regexp, error) {
	if len(pattern) > MaxRegexBytes {
		return nil, fmt.Errorf("regex pattern exceeds the %d byte limit", MaxRegexBytes)
	}
	r, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}
	return r, nil
}

func jsFillRune(fill string) rune {
	if fill == "" {
		return ' '
	}
	r, _ := utf8.DecodeRuneInString(fill)
	return r
}
func jsPadWith(value string, width int, fill rune, left bool) (string, error) {
	if width <= 0 {
		return value, nil
	}
	missing := width - utf8.RuneCountInString(value)
	if missing <= 0 {
		return value, nil
	}
	if missing > helperOutputLimit || len(value)+missing*utf8.RuneLen(fill) > helperOutputLimit {
		return "", fmt.Errorf("naming template output exceeds the %d byte limit", helperOutputLimit)
	}
	padding := strings.Repeat(string(fill), missing)
	if left {
		return padding + value, nil
	}
	return value + padding, nil
}
