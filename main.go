package pbsql

import (
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/jmoiron/sqlx"
)

// BuildCreateQuery accepts a target table name and a protobuf message and attempts to build a valid SQL insert statement for use
// with sqlx.Named, ignoring any struct fields with default values. Fields must be tagged with `db:""` in order to be
// included in the result string.
func BuildCreateQuery(target string, source interface{}) (string, []interface{}, error) {
	t := reflect.ValueOf(source).Elem()
	var cols strings.Builder
	var vals strings.Builder
	fmt.Fprintf(&cols, "INSERT INTO %s (", target)
	vals.WriteString("(")

	for i := 0; i < t.NumField(); i++ {
		valField := t.Field(i)
		typeField := t.Type().Field(i)
		typeName := valField.Type().Name()
		isPrimaryKey := typeField.Tag.Get("primary_key") != ""
		tag := typeField.Tag.Get("db")

		if notDefault(typeName, valField.Interface()) && tag != "" && !isPrimaryKey {
			if i != 0 {
				cols.WriteString(", ")
				vals.WriteString(", ")
			}
			cols.WriteString(tag)
			fmt.Fprintf(&vals, ":%s", tag)
		}
	}
	vals.WriteString(")")
	fmt.Fprintf(&cols, ") VALUES %s", vals.String())
	result := strings.ReplaceAll(cols.String(), "(, ", "(")
	return sqlx.Named(result, source)
}

// BuildDeleteQuery accepts a target table name and a protobuf message and attempts to build a valid SQL
// delete statement by utilizing struct tags to denote information such as database field names and
// whether something is a primary key. If successful, returns a SQL statement in the form of a string,
// a slice of args to interpolate, and a nil error.
//
// This function returns a nullsafe query if nullable struct fields are properly tagged as `nullable:"y"`.
//
// If an IsActive field is detected (is_active), this func returns an update statement that sets is_active to 0,
// otherwise it returns a delete statement
func BuildDeleteQuery(target string, source interface{}) (string, []interface{}, error) {
	v := reflect.ValueOf(source).Elem()
	t := v.Type()
	var builder strings.Builder

	isActive, hasIsActive := t.FieldByName("IsActive")
	if hasIsActive {
		dbName := isActive.Tag.Get("db")
		fmt.Fprintf(&builder, "UPDATE %s SET %s = :%s WHERE ", target, dbName, dbName)
	} else {
		fmt.Fprintf(&builder, "DELETE FROM %s WHERE ", target)
	}

	for i := 0; i < v.NumField(); i++ {
		typeField := t.Field(i)
		isPkey := typeField.Tag.Get("primary_key") != ""
		if isPkey {
			dbName := typeField.Tag.Get("db")
			fmt.Fprintf(&builder, "%s = :%s", dbName, dbName)
			break
		}
	}

	return sqlx.Named(builder.String(), source)
}

// BuildReadQuery accepts a target table name and a protobuf message and attempts to build a valid SQL select statement,
// ignoring any struct fields with default values when writing predicates. Fields must be tagged with `db:""` in order to be
// included in the result string.
//
// Returns a SQL statement as a string, a slice of args to interpolate, and an error
func BuildReadQuery(target string, source interface{}) (string, []interface{}, error) {
	nullHandler := "ifnull("
	if sqlDriver := os.Getenv("GRPC_SQL_DRIVER"); sqlDriver == "pgsql" {
		nullHandler = "coalesce("
	}

	t := reflect.ValueOf(source).Elem()

	var core strings.Builder
	var fields strings.Builder
	var predicate strings.Builder
	core.WriteString("SELECT ")
	predicate.WriteString(" WHERE true")

	for i := 0; i < t.NumField(); i++ {
		valField := t.Field(i)
		typeField := t.Type().Field(i)
		typeName := valField.Type().Name()
		dbName := typeField.Tag.Get("db")
		nullable := typeField.Tag.Get("nullable")

		if nullable != "" {
			fmt.Fprintf(&fields, "%s%s, %s) as %s, ", nullHandler, dbName, getDefault(typeName), dbName)
		} else if dbName != "" {
			fmt.Fprintf(&fields, "%s, ", dbName)
		}

		if valField.CanInterface() && notDefault(typeName, valField.Interface()) && dbName != "" {
			fmt.Fprintf(&predicate, " AND %s", dbName)
			if typeName == "string" {
				fmt.Fprintf(&predicate, " LIKE :%s", dbName)
			} else {
				fmt.Fprintf(&predicate, " = :%s", dbName)
			}
		}
	}

	fmt.Fprintf(&core, "%sFROM %s%s", fields.String(), target, predicate.String())
	result := strings.Replace(core.String(), ", FROM", " FROM", 1)
	return sqlx.Named(result, source)
}

// BuildUpdateQuery accepts a target table name `target`, a struct `source`, and a list of struct fields `fieldMask`
// and attempts to build a valid sql update statement for use with sqlx.Named, ignoring any struct fields not present
// in `fieldMask`. Struct fields must also be tagged with `db:""`, and the primary key should be tagged as
// `primary_key` otherwise this function will return an invalid query
func BuildUpdateQuery(target string, source interface{}, fieldMask map[string]int32) (string, []interface{}, error) {
	v := reflect.ValueOf(source).Elem()
	t := v.Type()

	var builder strings.Builder
	fmt.Fprintf(&builder, "UPDATE %s SET ", target)

	var predicate strings.Builder
	for i := 0; i < v.NumField(); i++ {
		valField := v.Field(i)
		typeField := t.Field(i)
		dbName := typeField.Tag.Get("db")

		if valField.CanInterface() && dbName != "" {
			isPrimaryKey := typeField.Tag.Get("primary_key") != ""
			if isPrimaryKey {
				fmt.Fprintf(&predicate, "WHERE %s = :%s", dbName, dbName)
			} else if _, ok := fieldMask[typeField.Name]; ok {
				fmt.Fprintf(&builder, "%s = :%s,", dbName, dbName)
			}
		}
	}

	builder.WriteString(predicate.String())
	result := strings.Replace(builder.String(), ", WHERE", " WHERE", 1)
	return sqlx.Named(result, source)
}

// `notDefault` checks if a value is set to it's unitialized default, e.g. whether or not an `int32` value is `0`
// returns `true` if not default.
func notDefault(typeName string, fieldVal interface{}) bool {
	switch typeName {
	case "int32":
		return fieldVal.(int32) != 0
	case "float64":
		return fieldVal.(float64) != 0
	case "string":
		return fieldVal.(string) != ""
	default:
		return fieldVal != nil
	}
}

// `getDefault` returns the unitialized value of a type for sql ifnull statements
func getDefault(typeName string) string {
	switch typeName {
	case "byte", "rune", "uint", "int", "uint8", "uint16", "uint32", "uint64", "int8", "int16", "int32", "int64":
		return "0"
	case "float32", "float64":
		return "0.0"
	case "bool":
		return "0"
	case "string":
		return "''"
	default:
		panic(fmt.Errorf("couldn't determine default value for provided type %s", typeName))
	}
}
