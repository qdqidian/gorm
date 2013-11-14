package gorm

import (
	"database/sql"
	"errors"

	"go/ast"
	"reflect"
	"regexp"

	"time"
)

type Model struct {
	data          interface{}
	do            *Do
	_cache_fields map[string][]Field
}

type Field struct {
	Name           string
	Value          interface{}
	SqlType        string
	DbName         string
	AutoCreateTime bool
	AutoUpdateTime bool
	IsPrimaryKey   bool
	IsBlank        bool

	beforeAssociation bool
	afterAssociation  bool
	foreignKey        string
}

func (m *Model) primaryKeyZero() bool {
	return m.primaryKeyValue() <= 0
}

func (m *Model) primaryKeyValue() int64 {
	if m.data == nil {
		return -1
	}
	data := reflect.Indirect(reflect.ValueOf(m.data))

	switch data.Kind() {
	case reflect.Array, reflect.Chan, reflect.Map, reflect.Ptr, reflect.Slice:
		return 0
	default:
		value := data.FieldByName(m.primaryKey())

		if value.IsValid() {
			switch value.Kind() {
			case reflect.Int, reflect.Int64, reflect.Int32:
				return value.Int()
			default:
				return 0
			}
		} else {
			return 0
		}
	}
}

func (m *Model) primaryKey() string {
	return "Id"
}

func (m *Model) primaryKeyDb() string {
	return toSnake(m.primaryKey())
}

func (m *Model) fields(operation string) (fields []Field) {
	if len(m._cache_fields[operation]) > 0 {
		return m._cache_fields[operation]
	}

	indirect_value := reflect.Indirect(reflect.ValueOf(m.data))
	if !indirect_value.IsValid() {
		return
	}

	typ := indirect_value.Type()
	for i := 0; i < typ.NumField(); i++ {
		p := typ.Field(i)
		if !p.Anonymous && ast.IsExported(p.Name) {
			var field Field
			field.Name = p.Name
			field.DbName = toSnake(p.Name)
			field.IsPrimaryKey = m.primaryKeyDb() == field.DbName
			value := indirect_value.FieldByName(p.Name)
			time_value, is_time := value.Interface().(time.Time)

			switch value.Kind() {
			case reflect.Int, reflect.Int64, reflect.Int32:
				field.IsBlank = value.Int() == 0
			case reflect.String:
				field.IsBlank = value.String() == ""
			case reflect.Slice:
				field.IsBlank = value.Len() == 0
			case reflect.Struct:
				if is_time {
					field.IsBlank = time_value.IsZero()
				} else {
					_, is_scanner := reflect.New(value.Type()).Interface().(sql.Scanner)

					if is_scanner {
						field.IsBlank = !value.FieldByName("Valid").Interface().(bool)
					} else {
						m := &Model{data: value.Interface(), do: m.do}
						fields := m.columnsHasValue("other")
						if len(fields) == 0 {
							field.IsBlank = true
						}
					}
				}
			}

			if is_time {
				field.AutoCreateTime = "created_at" == field.DbName
				field.AutoUpdateTime = "updated_at" == field.DbName

				switch operation {
				case "create":
					if (field.AutoCreateTime || field.AutoUpdateTime) && time_value.IsZero() {
						value.Set(reflect.ValueOf(time.Now()))
					}
				case "update":
					if field.AutoUpdateTime {
						value.Set(reflect.ValueOf(time.Now()))
					}
				}
			}

			field.Value = value.Interface()

			if is_time {
				field.SqlType = m.getSqlTag(field, p)
			} else if field.IsPrimaryKey {
				field.SqlType = m.getSqlTag(field, p)
			} else {
				field_value := reflect.Indirect(value)

				switch field_value.Kind() {
				case reflect.Slice:
					foreign_key := typ.Name() + "Id"
					if reflect.New(field_value.Type().Elem()).Elem().FieldByName(foreign_key).IsValid() {
						field.foreignKey = foreign_key
					}
					field.afterAssociation = true
				case reflect.Struct:
					_, is_scanner := reflect.New(field_value.Type()).Interface().(sql.Scanner)

					if is_scanner {
						field.SqlType = m.getSqlTag(field, p)
					} else {
						if indirect_value.FieldByName(p.Name + "Id").IsValid() {
							field.foreignKey = p.Name + "Id"
							field.beforeAssociation = true
						} else {
							foreign_key := typ.Name() + "Id"
							if reflect.New(field_value.Type()).Elem().FieldByName(foreign_key).IsValid() {
								field.foreignKey = foreign_key
							}
							field.afterAssociation = true
						}
					}
				default:
					field.SqlType = m.getSqlTag(field, p)
				}
			}

			fields = append(fields, field)
		}
	}

	if len(m._cache_fields) == 0 {
		m._cache_fields = map[string][]Field{}
	}
	m._cache_fields[operation] = fields
	return
}

func (m *Model) columnsHasValue(operation string) (fields []Field) {
	for _, field := range m.fields(operation) {
		if !field.IsBlank {
			fields = append(fields, field)
		}
	}
	return
}

func (m *Model) updatedColumnsAndValues(values map[string]interface{}) (results map[string]interface{}, any_updated bool) {
	if m.data == nil {
		return values, true
	}

	data := reflect.Indirect(reflect.ValueOf(m.data))
	for key, value := range values {
		field := data.FieldByName(snakeToUpperCamel(key))
		if field.IsValid() {
			if field.Interface() != value {
				switch field.Kind() {
				case reflect.Int, reflect.Int32, reflect.Int64:
					if field.Int() != reflect.ValueOf(value).Int() {
						any_updated = true
					}
					field.SetInt(reflect.ValueOf(value).Int())
				default:
					any_updated = true
					field.Set(reflect.ValueOf(value))
				}
			}
		}
	}

	if values["updated_at"] != nil && any_updated {
		setFieldValue(data.FieldByName("UpdatedAt"), time.Now())
	}
	return
}

func (m *Model) columnsAndValues(operation string) map[string]interface{} {
	results := map[string]interface{}{}

	if m.data != nil {
		for _, field := range m.fields(operation) {
			if !field.IsPrimaryKey && (len(field.SqlType) > 0) {
				results[field.DbName] = field.Value
			}
		}
	}
	return results
}

func (m *Model) hasColumn(name string) bool {
	if m.data == nil {
		return false
	}

	data := reflect.Indirect(reflect.ValueOf(m.data))
	if data.Kind() == reflect.Slice {
		return reflect.New(data.Type().Elem()).Elem().FieldByName(name).IsValid()
	} else {
		return data.FieldByName(name).IsValid()
	}
}

func (m *Model) ColumnAndValue(name string) (has_column bool, is_slice bool, value interface{}) {
	if m.data != nil {
		data := reflect.Indirect(reflect.ValueOf(m.data))
		if data.Kind() == reflect.Slice {
			has_column = reflect.New(data.Type().Elem()).Elem().FieldByName(name).IsValid()
			is_slice = true
		} else {
			if has_column = data.FieldByName(name).IsValid(); has_column {
				value = data.FieldByName(name).Interface()
			}
		}
	}
	return
}

func (m *Model) typeName() string {
	typ := reflect.Indirect(reflect.ValueOf(m.data)).Type()
	if typ.Kind() == reflect.Slice {
		typ = typ.Elem()
	}

	return typ.Name()
}

func (m *Model) tableName() (str string) {
	if m.data == nil {
		m.do.err(errors.New("Model haven't been set"))
		return
	}

	fm := reflect.Indirect(reflect.ValueOf(m.data)).MethodByName("TableName")
	if fm.IsValid() {
		if v := fm.Call([]reflect.Value{}); len(v) > 0 {
			if result, ok := v[0].Interface().(string); ok {
				return result
			}
		}
	}

	str = toSnake(m.typeName())

	if !singularTableName {
		pluralMap := map[string]string{"ch": "ches", "ss": "sses", "sh": "shes", "day": "days", "y": "ies", "x": "xes", "s?": "s"}
		for key, value := range pluralMap {
			reg := regexp.MustCompile(key + "$")
			if reg.MatchString(str) {
				return reg.ReplaceAllString(str, value)
			}
		}
	}

	return
}

func (m *Model) callMethod(method string) {
	if m.data == nil || m.do.chain.hasError() {
		return
	}

	fm := reflect.ValueOf(m.data).MethodByName(method)
	if fm.IsValid() {
		if v := fm.Call([]reflect.Value{}); len(v) > 0 {
			if verr, ok := v[0].Interface().(error); ok {
				m.do.err(verr)
			}
		}
	}
	return
}

func (m *Model) setValueByColumn(name string, value interface{}, out interface{}) {
	data := reflect.Indirect(reflect.ValueOf(out))
	setFieldValue(data.FieldByName(snakeToUpperCamel(name)), value)
}

func (m *Model) beforeAssociations() (fields []Field) {
	for _, field := range m.fields("null") {
		if field.beforeAssociation && !field.IsBlank {
			fields = append(fields, field)
		}
	}
	return
}

func (m *Model) afterAssociations() (fields []Field) {
	for _, field := range m.fields("null") {
		if field.afterAssociation && !field.IsBlank {
			fields = append(fields, field)
		}
	}
	return
}

func (m *Model) getSqlTag(field Field, struct_field reflect.StructField) string {
	column := getInterfaceValue(field.Value)
	typ, addational_typ, size := parseSqlTag(struct_field.Tag.Get(tagIdentifier))

	if typ == "-" {
		return ""
	}

	if len(typ) == 0 {
		if field.IsPrimaryKey {
			typ = m.do.chain.d.dialect.PrimaryKeyTag(column, size)
		} else {
			typ = m.do.chain.d.dialect.SqlTag(column, size)
		}
	}

	if len(addational_typ) > 0 {
		typ = typ + " " + addational_typ
	}
	return typ
}
