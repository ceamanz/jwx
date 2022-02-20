package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/lestrrat-go/codegen"
)

func main() {
	if err := _main(); err != nil {
		log.Printf("%s", err)
		os.Exit(1)
	}
}

func _main() error {
	codegen.RegisterZeroVal(`jwa.KeyEncryptionAlgorithm`, `""`)
	codegen.RegisterZeroVal(`jwa.CompressionAlgorithm`, `jwa.NoCompress`)
	codegen.RegisterZeroVal(`jwa.ContentEncryptionAlgorithm`, `""`)
	var objectsFile = flag.String("objects", "objects.yml", "")
	flag.Parse()
	jsonSrc, err := yaml2json(*objectsFile)
	if err != nil {
		return err
	}

	var object codegen.Object
	if err := json.NewDecoder(bytes.NewReader(jsonSrc)).Decode(&object); err != nil {
		return fmt.Errorf(`failed to decode %q: %w`, *objectsFile, err)
	}

	object.Organize()
	return generateHeaders(&object)
}

func yaml2json(fn string) ([]byte, error) {
	in, err := os.Open(fn)
	if err != nil {
		return nil, fmt.Errorf(`failed to open %q: %w`, fn, err)
	}
	defer in.Close()

	var v interface{}
	if err := yaml.NewDecoder(in).Decode(&v); err != nil {
		return nil, fmt.Errorf(`failed to decode %q: %w`, fn, err)
	}

	return json.Marshal(v)
}

func boolFromField(f codegen.Field, field string) (bool, error) {
	v, ok := f.Extra(field)
	if !ok {
		return false, fmt.Errorf("%q does not exist in %q", field, f.Name(true))
	}

	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%q should be a bool in %q", field, f.Name(true))
	}
	return b, nil
}

func fieldHasAccept(f codegen.Field) bool {
	v, _ := boolFromField(f, "hasAccept")
	return v
}

func PointerElem(f codegen.Field) string {
	return strings.TrimPrefix(f.Type(), `*`)
}

func fieldStorageType(s string) string {
	if fieldStorageTypeIsIndirect(s) {
		return `*` + s
	}
	return s
}

func fieldStorageTypeIsIndirect(s string) bool {
	return !(s == "jwk.Key" || s == "jwk.ECDSAPublicKey" || strings.HasPrefix(s, `*`) || strings.HasPrefix(s, `[]`))
}

func generateHeaders(obj *codegen.Object) error {
	var buf bytes.Buffer

	o := codegen.NewOutput(&buf)
	o.L("// This file is auto-generated by jwe/internal/cmd/genheaders/main.go. DO NOT EDIT")
	o.LL("package jwe")

	o.LL("const (")
	for _, f := range obj.Fields() {
		o.L("%sKey = %q", f.Name(true), f.JSON())
	}
	o.L(")") // end const

	o.LL("// Headers describe a standard Header set.")
	o.L("type Headers interface {")
	o.L("json.Marshaler")
	o.L("json.Unmarshaler")
	// These are the basic values that most jws have
	for _, f := range obj.Fields() {
		o.L("%s() %s", f.GetterMethod(true), f.Type()) //PointerElem())
	}

	// These are used to iterate through all keys in a header
	o.L("Iterate(ctx context.Context) Iterator")
	o.L("Walk(ctx context.Context, v Visitor) error")
	o.L("AsMap(ctx context.Context) (map[string]interface{}, error)")

	// These are used to access a single element by key name
	o.L("Get(string) (interface{}, bool)")
	o.L("Set(string, interface{}) error")
	o.L("Remove(string) error")

	// These are used to deal with encoded headers
	o.L("Encode() ([]byte, error)")
	o.L("Decode([]byte) error")

	// Access private parameters
	o.L("// PrivateParams returns the map containing the non-standard ('private') parameters")
	o.L("// in the associated header. WARNING: DO NOT USE PrivateParams()")
	o.L("// IF YOU HAVE CONCURRENT CODE ACCESSING THEM. Use AsMap() to")
	o.L("// get a copy of the entire header instead")
	o.L("PrivateParams() map[string]interface{}")

	o.L("Clone(context.Context) (Headers, error)")
	o.L("Copy(context.Context, Headers) error")
	o.L("Merge(context.Context, Headers) (Headers, error)")

	o.L("}")

	o.LL("type stdHeaders struct {")
	for _, f := range obj.Fields() {
		if c := f.Comment(); c != "" {
			o.L("%s %s // %s", f.Name(false), fieldStorageType(f.Type()), c)
		} else {
			o.L("%s %s", f.Name(false), fieldStorageType(f.Type()))
		}
	}
	o.L("privateParams map[string]interface{}")
	o.L("mu *sync.RWMutex")
	o.L("}") // end type StandardHeaders

	o.LL("func NewHeaders() Headers {")
	o.L("return &stdHeaders{")
	o.L("mu: &sync.RWMutex{},")
	o.L("privateParams: map[string]interface{}{},")
	o.L("}")
	o.L("}")

	for _, f := range obj.Fields() {
		o.LL("func (h *stdHeaders) %s() %s{", f.GetterMethod(true), f.Type())
		o.L("h.mu.RLock()")
		o.L("defer h.mu.RUnlock()")
		if !fieldStorageTypeIsIndirect(f.Type()) {
			o.L("return h.%s", f.Name(false))
		} else {
			o.L("if h.%s == nil {", f.Name(false))
			o.L("return %s", codegen.ZeroVal(f.Type()))
			o.L("}")
			o.L("return *(h.%s)", f.Name(false))
		}
		o.L("}") // func (h *stdHeaders) %s() %s
	}

	// Generate a function that iterates through all of the keys
	// in this header.
	o.LL("func (h *stdHeaders) makePairs() []*HeaderPair {")
	o.L("h.mu.RLock()")
	o.L("defer h.mu.RUnlock()")
	// NOTE: building up an array is *slow*?
	o.L("var pairs []*HeaderPair")
	for _, f := range obj.Fields() {
		o.L("if h.%s != nil {", f.Name(false))
		if fieldStorageTypeIsIndirect(f.Type()) {
			o.L("pairs = append(pairs, &HeaderPair{Key: %sKey, Value: *(h.%s)})", f.Name(true), f.Name(false))
		} else {
			o.L("pairs = append(pairs, &HeaderPair{Key: %sKey, Value: h.%s})", f.Name(true), f.Name(false))
		}
		o.L("}")
	}
	o.L("for k, v := range h.privateParams {")
	o.L("pairs = append(pairs, &HeaderPair{Key: k, Value: v})")
	o.L("}")
	o.L("return pairs")
	o.L("}") // end of (h *stdHeaders) iterate(...)

	o.LL("func (h *stdHeaders) PrivateParams() map[string]interface{} {")
	o.L("h.mu.RLock()")
	o.L("defer h.mu.RUnlock()")
	o.L("return h.privateParams")
	o.L("}")

	o.LL("func (h *stdHeaders) Get(name string) (interface{}, bool) {")
	o.L("h.mu.RLock()")
	o.L("defer h.mu.RUnlock()")
	o.L("switch name {")
	for _, f := range obj.Fields() {
		o.L("case %sKey:", f.Name(true))
		o.L("if h.%s == nil {", f.Name(false))
		o.L("return nil, false")
		o.L("}")
		if fieldStorageTypeIsIndirect(f.Type()) {
			o.L("return *(h.%s), true", f.Name(false))
		} else {
			o.L("return h.%s, true", f.Name(false))
		}
	}
	o.L("default:")
	o.L("v, ok := h.privateParams[name]")
	o.L("return v, ok")
	o.L("}") // end switch name
	o.L("}") // func (h *stdHeaders) Get(name string) (interface{}, bool)

	o.LL("func (h *stdHeaders) Set(name string, value interface{}) error {")
	o.L("h.mu.Lock()")
	o.L("defer h.mu.Unlock()")
	o.L("return h.setNoLock(name, value)")
	o.L("}")

	o.LL("func (h *stdHeaders) setNoLock(name string, value interface{}) error {")
	o.L("switch name {")
	for _, f := range obj.Fields() {
		o.L("case %sKey:", f.Name(true))
		if fieldHasAccept(f) {
			o.L("var acceptor %s", PointerElem(f))
			o.L("if err := acceptor.Accept(value); err != nil {")
			o.L("return errors.Wrapf(err, `invalid value for %%s key`, %sKey)", f.Name(true))
			o.L("}") // end if err := h.%s.Accept(value)
			o.L("h.%s = &acceptor", f.Name(false))
			o.L("return nil")
		} else {
			o.L("if v, ok := value.(%s); ok {", f.Type())
			if f.Name(false) == "contentEncryption" {
				// check for non-empty string, because empty content encryption is just baaaaaad
				o.L("if v == \"\" {")
				o.L("return errors.New(`%#v field cannot be an empty string`)", f.JSON())
				o.L("}")
			}

			if fieldStorageTypeIsIndirect(f.Type()) {
				o.L("h.%s = &v", f.Name(false))
			} else {
				o.L("h.%s = v", f.Name(false))
			}
			o.L("return nil")
			o.L("}") // end if v, ok := value.(%s)
			o.L("return errors.Errorf(`invalid value for %%s key: %%T`, %sKey, value)", f.Name(true))
		}
	}
	o.L("default:")
	o.L("if h.privateParams == nil {")
	o.L("h.privateParams = map[string]interface{}{}")
	o.L("}") // end if h.privateParams == nil
	o.L("h.privateParams[name] = value")
	o.L("}") // end switch name
	o.L("return nil")
	o.L("}") // end func (h *stdHeaders) Set(name string, value interface{})

	o.LL("func (h *stdHeaders) Remove(key string) error {")
	o.L("h.mu.Lock()")
	o.L("defer h.mu.Unlock()")
	o.L("switch key {")
	for _, f := range obj.Fields() {
		o.L("case %sKey:", f.Name(true))
		o.L("h.%s = nil", f.Name(false))
	}
	o.L("default:")
	o.L("delete(h.privateParams, key)")
	o.L("}")
	o.L("return nil") // currently unused, but who knows
	o.L("}")

	o.LL("func (h *stdHeaders) UnmarshalJSON(buf []byte) error {")
	for _, f := range obj.Fields() {
		o.L("h.%s = nil", f.Name(false))
	}

	o.L("dec := json.NewDecoder(bytes.NewReader(buf))")
	o.L("LOOP:")
	o.L("for {")
	o.L("tok, err := dec.Token()")
	o.L("if err != nil {")
	o.L("return errors.Wrap(err, `error reading token`)")
	o.L("}")
	o.L("switch tok := tok.(type) {")
	o.L("case json.Delim:")
	o.L("// Assuming we're doing everything correctly, we should ONLY")
	o.L("// get either '{' or '}' here.")
	o.L("if tok == '}' { // End of object")
	o.L("break LOOP")
	o.L("} else if tok != '{' {")
	o.L("return errors.Errorf(`expected '{', but got '%%c'`, tok)")
	o.L("}")
	o.L("case string: // Objects can only have string keys")
	o.L("switch tok {")

	for _, f := range obj.Fields() {
		if f.Type() == "string" {
			o.L("case %sKey:", f.Name(true))
			o.L("if err := json.AssignNextStringToken(&h.%s, dec); err != nil {", f.Name(false))
			o.L("return errors.Wrapf(err, `failed to decode value for key %%s`, %sKey)", f.Name(true))
			o.L("}")
		} else if f.Type() == "[]byte" {
			o.L("case %sKey:", f.Name(true))
			o.L("if err := json.AssignNextBytesToken(&h.%s, dec); err != nil {", f.Name(false))
			o.L("return errors.Wrapf(err, `failed to decode value for key %%s`, %sKey)", f.Name(true))
			o.L("}")
		} else if f.Type() == "jwk.Key" {
			o.L("case %sKey:", f.Name(true))
			o.L("var buf json.RawMessage")
			o.L("if err := dec.Decode(&buf); err != nil {")
			o.L("return errors.Wrapf(err, `failed to decode value for key %%s`, %sKey)", f.Name(true))
			o.L("}")
			o.L("key, err := jwk.ParseKey(buf)")
			o.L("if err != nil {")
			o.L("return errors.Wrapf(err, `failed to parse JWK for key %%s`, %sKey)", f.Name(true))
			o.L("}")
			o.L("h.%s = key", f.Name(false))
		} else if strings.HasPrefix(f.Type(), "[]") {
			o.L("case %sKey:", f.Name(true))
			o.L("var decoded %s", f.Type())
			o.L("if err := dec.Decode(&decoded); err != nil {")
			o.L("return errors.Wrapf(err, `failed to decode value for key %%s`, %sKey)", f.Name(true))
			o.L("}")
			o.L("h.%s = decoded", f.Name(false))
		} else {
			o.L("case %sKey:", f.Name(true))
			o.L("var decoded %s", f.Type())
			o.L("if err := dec.Decode(&decoded); err != nil {")
			o.L("return errors.Wrapf(err, `failed to decode value for key %%s`, %sKey)", f.Name(true))
			o.L("}")
			o.L("h.%s = &decoded", f.Name(false))
		}
	}
	o.L("default:")
	o.L("decoded, err := registry.Decode(dec, tok)")
	o.L("if err != nil {")
	o.L("return err")
	o.L("}")
	o.L("h.setNoLock(tok, decoded)")
	o.L("}")
	o.L("default:")
	o.L("return errors.Errorf(`invalid token %%T`, tok)")
	o.L("}")
	o.L("}")

	o.L("return nil")
	o.L("}")

	o.LL("func (h stdHeaders) MarshalJSON() ([]byte, error) {")
	o.L("data := make(map[string]interface{})")
	o.L("fields := make([]string, 0, %d)", len(obj.Fields()))
	o.L("for _, pair := range h.makePairs() {")
	o.L("fields = append(fields, pair.Key.(string))")
	o.L("data[pair.Key.(string)] = pair.Value")
	o.L("}")
	o.LL("sort.Strings(fields)")
	o.L("buf := pool.GetBytesBuffer()")
	o.L("defer pool.ReleaseBytesBuffer(buf)")
	o.L("buf.WriteByte('{')")
	o.L("enc := json.NewEncoder(buf)")
	o.L("for i, f := range fields {")
	o.L("if i > 0 {")
	o.L("buf.WriteRune(',')")
	o.L("}")
	o.L("buf.WriteRune('\"')")
	o.L("buf.WriteString(f)")
	o.L("buf.WriteString(`\":`)")
	o.L("v := data[f]")
	o.L("switch v := v.(type) {")
	o.L("case []byte:")
	o.L("buf.WriteRune('\"')")
	o.L("buf.WriteString(base64.EncodeToString(v))")
	o.L("buf.WriteRune('\"')")
	o.L("default:")
	o.L("if err := enc.Encode(v); err != nil {")
	o.L("errors.Errorf(`failed to encode value for field %%s`, f)")
	o.L("}")
	o.L("buf.Truncate(buf.Len()-1)")
	o.L("}")
	o.L("}")
	o.L("buf.WriteByte('}')")
	o.L("ret := make([]byte, buf.Len())")
	o.L("copy(ret, buf.Bytes())")
	o.L("return ret, nil")
	o.L("}")

	if err := o.WriteFile(`headers_gen.go`, codegen.WithFormatCode(true)); err != nil {
		if cfe, ok := err.(codegen.CodeFormatError); ok {
			fmt.Fprint(os.Stderr, cfe.Source())
		}
		return fmt.Errorf(`failed to write to headers_gen.go: %w`, err)
	}
	return nil
}
