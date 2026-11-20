package service

import (
	"slices"
	"strconv"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Request messages name their fields like the columns they are stored in, so
// the helpers below turn a message into a sync payload (column -> value) by
// reflection instead of one hand-written line per field.

// createPayload returns the payload for a new row from the listed fields of
// a request. Fields with presence (optional numbers) are included only when
// they were sent, so an unset value is stored as NULL ("not recorded");
// every other field is written with the value it has.
func createPayload(m protoreflect.Message, cols []string) (map[string]any, error) {
	fields := map[string]any{}
	for _, c := range cols {
		fd := m.Descriptor().Fields().ByName(protoreflect.Name(c))
		if fd.HasPresence() && !m.Has(fd) {
			continue
		}
		v, err := payloadValue(fd, m)
		if err != nil {
			return nil, err
		}
		fields[c] = v
	}
	return fields, nil
}

// updatePayload returns the payload of a partial update: the subset of the
// updatable fields that the request asks to change.
//
// With a field mask exactly the masked fields are written, including to
// their zero value (an unset optional number or timestamp is cleared).
// Without one, the fields that carry a value are written: a field left at
// its zero value counts as not sent. An update that would change nothing is
// rejected.
func updatePayload(m protoreflect.Message, mask *fieldmaskpb.FieldMask, updatable []string) (map[string]any, error) {
	masked := len(mask.GetPaths()) > 0
	for _, p := range mask.GetPaths() {
		if !slices.Contains(updatable, p) {
			return nil, invalid("update_mask: " + strconv.Quote(p) + " cannot be updated (updatable: " +
				strings.Join(updatable, ", ") + ")")
		}
	}
	fields := map[string]any{}
	for _, c := range updatable {
		fd := m.Descriptor().Fields().ByName(protoreflect.Name(c))
		if (masked && !slices.Contains(mask.GetPaths(), c)) || (!masked && !m.Has(fd)) {
			continue
		}
		v, err := payloadValue(fd, m)
		if err != nil {
			return nil, err
		}
		fields[c] = v
	}
	if len(fields) == 0 {
		return nil, invalid("no fields to update")
	}
	return fields, nil
}

// payloadValue converts one field to the form sync payloads use: booleans
// as 0/1, enums as numbers, timestamps as ISO strings ("" = not set), and
// nil for an optional number that is not set.
func payloadValue(fd protoreflect.FieldDescriptor, m protoreflect.Message) (any, error) {
	v := m.Get(fd)
	switch fd.Kind() {
	case protoreflect.MessageKind:
		ts, _ := v.Message().Interface().(*timestamppb.Timestamp)
		if !m.Has(fd) {
			ts = nil
		}
		return tsOrEmpty(ts), nil
	case protoreflect.EnumKind:
		if fd.Enum().Values().ByNumber(v.Enum()) == nil {
			return nil, invalid(string(fd.Name()) + ": unknown value " + itoa(int64(v.Enum())))
		}
		return int32(v.Enum()), nil
	case protoreflect.BoolKind:
		return b2i(v.Bool()), nil
	}
	if fd.HasPresence() && !m.Has(fd) {
		return nil, nil
	}
	return v.Interface(), nil
}

// requireValue rejects a payload that would blank one of the given columns:
// "" for text and dates, 0 for numbers.
func requireValue(fields map[string]any, cols ...string) error {
	for _, c := range cols {
		switch v := fields[c].(type) {
		case string:
			if v == "" {
				return invalid(c + " must not be empty")
			}
		case int32:
			if v == 0 {
				return invalid(c + " must be set")
			}
		}
	}
	return nil
}
