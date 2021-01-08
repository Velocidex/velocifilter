// Utility functions for extracting and validating inputs to functions
// and plugins.
package arg_parser

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/Velocidex/ordereddict"
	errors "github.com/pkg/errors"
	"www.velocidex.com/golang/vfilter/types"
	"www.velocidex.com/golang/vfilter/utils"
)

type tmpTypes struct {
	any    types.Any
	stored types.StoredQuery
	lazy   types.LazyExpr
}

var (
	// A bit of a hack to get the type of interface fields
	testType        = tmpTypes{}
	anyType         = reflect.ValueOf(testType).Type().Field(0).Type
	storedQueryType = reflect.ValueOf(testType).Type().Field(1).Type
	lazyExprType    = reflect.ValueOf(testType).Type().Field(2).Type
)

// Structs may tag fields with this name to control parsing.
const tagName = "vfilter"

// Extract the content of args into the struct value. Value's members
// should be tagged with the "vfilter" tag.

// This function makes it very easy to extract args into VQL plugins
// or functions. Simply declare an args struct:

// type MyArgs struct {
//    Field string `vfilter:"required,field=field_name"`
// }

// And parse the struct using this function:
// myarg := &MyArgs{}
// err := vfilter.ExtractArgs(scope, args, myarg)

// We will raise an error if a required field is missing or has the
// wrong type of args.

// NOTE: In order for the field to be populated by this function, the
// field must be exported (i.e. name begins with cap) and it must have
// vfilter tags.

// FIXME - this code can be better optimized.
func ExtractArgs(scope types.Scope, args *ordereddict.Dict, value interface{}) error {

	// Make a copy of the args that we can modify so we can ensure
	// they are all provided properly.
	arg_map := *args.ToDict()
	v := reflect.ValueOf(value)

	if v.Type().Kind() == reflect.Ptr {
		v = v.Elem()
	}

	for i := 0; i < v.NumField(); i++ {
		// Get the field tag value
		field_types_value := v.Type().Field(i)
		tag := field_types_value.Tag.Get(tagName)

		// Skip if tag is not defined or ignored
		if tag == "" || tag == "-" {
			continue
		}

		directives := strings.Split(tag, ",")
		options := make(map[string]string)
		for _, directive := range directives {
			if strings.Contains(directive, "=") {
				components := strings.Split(directive, "=")
				if len(components) >= 2 {
					options[components[0]] = components[1]
				}
			}

		}
		field_name, pres := options["field"]
		if !pres {
			field_name = field_types_value.Name
		}

		if field_name == "" {
			panic("Fields can not be empty")
		}

		// Get the field. If it is not present but is
		// required, it is an error.
		arg, pres := arg_map[field_name]
		if !pres {
			if utils.InString(&directives, "required") {
				return errors.New(fmt.Sprintf(
					"Field %s is required.", field_name))
			}

			// Field is optional and not provided.
			continue
		} else {
			// Remove the field from the map
			delete(arg_map, field_name)
		}

		// Now cast the arg into the correct type to go into
		// the value output struct.
		field_value := v.Field(field_types_value.Index[0])
		if !field_value.IsValid() || !field_value.CanSet() {
			return errors.New(fmt.Sprintf(
				"Field %s is unsettable.", field_name))
		}

		// The plugin may specify the arg as being a LazyExpr,
		// in which case it is completely up to it to evaluate
		// the expression (if at all).
		if field_types_value.Type == lazyExprType {
			// It is not a types.LazyExpr, we wrap it in one.
			field_value.Set(reflect.ValueOf(ToLazyExpr(scope, arg)))
			continue
		}

		// The target field is a types.StoredQuery - check that what
		// was provided is actually one of those.
		if field_types_value.Type == storedQueryType {
			field_value.Set(reflect.ValueOf(ToStoredQuery(arg)))
			continue
		}

		// From here below, arg has to be non-lazy so we can
		// deal with its materialized form.
		lazy_arg, ok := arg.(types.LazyExpr)
		if ok {
			arg = lazy_arg.Reduce()
		}
		// The target field is an types.Any type - just assign it directly.
		if field_types_value.Type == anyType {
			field_value.Set(reflect.ValueOf(arg))
			continue
		}

		// Supported target field types:
		switch field_types_value.Type.Kind() {

		// It is a slice.
		case reflect.Slice:
			new_value, pres := _ExtractStringArray(scope, arg)
			if pres {
				field_value.Set(reflect.ValueOf(new_value))
			}

		case reflect.String:
			// If we expect a string and we get an array
			// of length 1 of strings, we just take the
			// first element. This allows us to simply
			// coerce a query into a variable without
			// using get.
			if utils.IsArray(arg) {
				new_value, pres := _ExtractStringArray(scope, arg)
				if pres && len(new_value) == 1 {
					field_value.Set(reflect.ValueOf(new_value[0]))
					continue
				}
			}

			switch t := arg.(type) {
			case string:
				field_value.Set(reflect.ValueOf(t))

			case types.Null, *types.Null, nil:
				continue
			default:
				field_value.Set(reflect.ValueOf(
					fmt.Sprintf("%s", arg)))
			}

		case reflect.Bool:
			field_value.Set(reflect.ValueOf(scope.Bool(arg)))

		case reflect.Float64:
			a, ok := utils.ToFloat(arg)
			if ok {
				field_value.Set(reflect.ValueOf(a))
			} else {
				return errors.New(fmt.Sprintf(
					"Field %s should be a float not %t.",
					field_types_value.Name, arg))
			}
		case reflect.Int64:
			a, ok := utils.ToInt64(arg)
			if ok {
				field_value.Set(reflect.ValueOf(a))
			} else {
				return errors.New(fmt.Sprintf(
					"Field %s should be an int.",
					field_types_value.Name))
			}
		case reflect.Uint64:
			a, ok := utils.ToInt64(arg)
			if ok {
				field_value.Set(reflect.ValueOf(uint64(a)))
			} else {
				return errors.New(fmt.Sprintf(
					"Field %s should be an int.",
					field_types_value.Name))
			}
		case reflect.Int:
			a, ok := utils.ToInt64(arg)
			if ok {
				field_value.Set(reflect.ValueOf(int(a)))
			} else {
				return errors.New(fmt.Sprintf(
					"Field %s should be an int.",
					field_types_value.Name))
			}
		default:
			if utils.InString(&directives, "required") {
				return errors.New(fmt.Sprintf(
					"Field %s is required.", field_name))
			}
			scope.Log("Unsupported type for field %v", field_name)
		}
	}

	// If we get here and there are some args left over, they were
	// not recognized.
	if len(arg_map) != 0 {
		for k, _ := range arg_map {
			scope.Log("Extra unrecognized arg: %s", k)
		}
	}

	return nil
}

// Try to retrieve an arg name from the Dict of args. Coerce the arg
// into something resembling a list of strings.
func _ExtractStringArray(scope types.Scope, arg types.Any) ([]string, bool) {
	var result []string

	// Handle potentially lazy args.
	lazy_arg, ok := arg.(types.LazyExpr)
	if ok {
		arg = lazy_arg.Reduce()
	}

	slice := reflect.ValueOf(arg)

	// A slice of strings.
	if slice.Type().Kind() == reflect.Slice {
		for i := 0; i < slice.Len(); i++ {
			value := slice.Index(i).Interface()
			item, ok := utils.ToString(value)
			if ok {
				result = append(result, item)
				continue
			}

			// If is a dict with only one member just use
			// that one.
			members := scope.GetMembers(value)
			if len(members) == 1 {
				member, ok := scope.Associative(value, members[0])
				if ok {
					item, ok := utils.ToString(member)
					if ok {
						result = append(result, item)
					}
				}
			}

			// Represent the value as a string.
			result = append(result, fmt.Sprintf("%v", value))
		}
		return result, true
	}

	// A single string just expands into a list of length 1.
	item, ok := utils.ToString(slice.Interface())
	if !ok {
		// If it has no StringProtocol fall back to golang
		// default.
		item = fmt.Sprintf("%v", slice.Interface())
	}
	result = append(result, item)
	return result, true
}

// Convert a type to a stored query
func ToStoredQuery(arg types.Any) types.StoredQuery {
	switch t := arg.(type) {
	case types.LazyExpr:
		return ToStoredQuery(t.Reduce())

	case types.StoredQuery:
		return t
	default:
		return &storedQueryWrapper{arg}
	}
}

type storedQueryWrapper struct {
	value types.Any
}

func (self *storedQueryWrapper) Eval(ctx context.Context, scope types.Scope) <-chan types.Row {
	output_chan := make(chan types.Row)
	go func() {
		defer close(output_chan)

		slice := reflect.ValueOf(self.value)
		if slice.Type().Kind() == reflect.Slice {
			for i := 0; i < slice.Len(); i++ {
				value := slice.Index(i).Interface()
				if !types.IsNullObject(value) {
					select {
					case <-ctx.Done():
						return
					case output_chan <- self.toRow(scope, value):
					}
				}
			}
		} else {
			row_value := self.toRow(scope, self.value)
			if !types.IsNullObject(row_value) {
				select {
				case <-ctx.Done():
					return
				case output_chan <- row_value:
				}
			}
		}

	}()
	return output_chan
}

func (self *storedQueryWrapper) toRow(scope types.Scope, value types.Any) types.Row {
	if types.IsNullObject(value) {
		return types.Null{}
	}

	members := scope.GetMembers(value)
	if len(members) > 0 {
		return value
	}

	return ordereddict.NewDict().Set("_value", value)
}

func ToLazyExpr(scope types.Scope, arg types.Any) types.LazyExpr {
	// Fixme...
	ctx := context.Background()

	switch t := arg.(type) {
	case types.LazyExpr:
		return t

	case types.StoredQuery:
		return &storedQueryWrapperLazyExpression{
			ctx: ctx, scope: scope, query: t}
	default:
		return &lazyExpressionWrapper{arg}
	}
}

type storedQueryWrapperLazyExpression struct {
	ctx   context.Context
	scope types.Scope
	query types.StoredQuery
}

func (self *storedQueryWrapperLazyExpression) ReduceWithScope(scope types.Scope) types.Any {
	result := []types.Row{}

	for row := range self.query.Eval(self.ctx, scope) {
		result = append(result, row)
	}

	return result
}

func (self *storedQueryWrapperLazyExpression) Reduce() types.Any {
	result := []types.Row{}

	for row := range self.query.Eval(self.ctx, self.scope) {
		result = append(result, row)
	}

	return result
}

type lazyExpressionWrapper struct {
	value types.Any
}

func (self *lazyExpressionWrapper) ReduceWithScope(scope types.Scope) types.Any {
	return self.value
}

func (self *lazyExpressionWrapper) Reduce() types.Any {
	return self.value
}

type StringProtocol interface {
	ToString() string
}