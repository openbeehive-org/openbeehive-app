package service

import (
	"google.golang.org/protobuf/reflect/protoreflect"
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
