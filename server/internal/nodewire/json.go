package nodewire

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"
)

// StrictJSON rejects duplicate keys at every nesting level as well as unknown
// fields. All callers also impose a byte limit before parsing.
func StrictJSON(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 10 {
			return errors.New("JSON nesting too deep")
		}
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'):
			keys := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := k.(string)
				if !ok || keys[s] {
					return errors.New("duplicate JSON field")
				}
				keys[s] = true
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
			_, err = d.Token()
			return err
		case json.Delim('['):
			for d.More() {
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	return shape(raw, reflect.TypeOf(out).Elem())
}

func shape(raw []byte, t reflect.Type) error {
	if t == reflect.TypeOf(time.Time{}) {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return errors.New("null timestamp")
		}
		return nil
	}
	if t.Kind() == reflect.Pointer {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil
		}
		return shape(raw, t.Elem())
	}
	if t == reflect.TypeOf(json.RawMessage{}) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return errors.New("object required")
		}
		allowed := map[string]bool{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue
			}
			tag := strings.Split(f.Tag.Get("json"), ",")
			key := tag[0]
			if key == "-" {
				continue
			}
			if key == "" {
				key = f.Name
			}
			allowed[key] = true
			value, present := fields[key]
			if !present {
				if len(tag) > 1 && tag[1] == "omitempty" {
					continue
				}
				return errors.New("required JSON field missing")
			}
			if err := shape(value, f.Type); err != nil {
				return err
			}
		}
		for key := range fields {
			if !allowed[key] {
				return errors.New("unexpected JSON field")
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for _, v := range values {
			if err := shape(v, t.Elem()); err != nil {
				return err
			}
		}
	default:
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return errors.New("null JSON field")
		}
	}
	return nil
}
