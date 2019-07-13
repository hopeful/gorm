package gorm

import (
	"database/sql"
	"errors"
	"go/ast"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/jinzhu/inflection"
)

// DefaultTableNameHandler default table name handler
var DefaultTableNameHandler = func(db *DB, defaultTableName string) string {
	return defaultTableName
}

// 缓存解析好的struct，安全Map
var modelStructsMap sync.Map

// ModelStruct model definition  struct模型解析 映射到数据库哪张表
type ModelStruct struct {
	PrimaryFields []*StructField // 主键字段
	StructFields  []*StructField // struct字段
	ModelType     reflect.Type   // 数据类型

	defaultTableName string     // 默认的数据库表名
	l                sync.Mutex //互斥锁
}

// TableName returns model's table name  返回model对应的表名规则
func (s *ModelStruct) TableName(db *DB) string {
	s.l.Lock()
	defer s.l.Unlock()

	if s.defaultTableName == "" && db != nil && s.ModelType != nil {
		// Set default table name
		if tabler, ok := reflect.New(s.ModelType).Interface().(tabler); ok {
			s.defaultTableName = tabler.TableName()
		} else {
			tableName := ToTableName(s.ModelType.Name())
			if db == nil || (db.parent != nil && !db.parent.singularTable) {
				tableName = inflection.Plural(tableName)
			}
			s.defaultTableName = tableName
		}
	}

	return DefaultTableNameHandler(db, s.defaultTableName)
}

// StructField model field's struct definition
// 对struct结构体字段与数据库字段，关系的映射
type StructField struct {
	DBName          string // 数据库名称
	Name            string // 字段名字
	Names           []string
	IsPrimaryKey    bool                // 是否主键
	IsNormal        bool                // 是否为普通字段
	IsIgnored       bool                // 是否为忽视的字段
	IsScanner       bool                // 是否
	HasDefaultValue bool                // 是否默认值
	Tag             reflect.StructTag   //	结构体Tag
	TagSettings     map[string]string   //	Tag 配置的属性
	Struct          reflect.StructField // 字段类型是struct类型
	IsForeignKey    bool                // 是否外键
	Relationship    *Relationship       // 关联关系

	tagSettingsLock sync.RWMutex //tag 配置读写锁
}

// TagSettingsSet Sets a tag in the tag settings map
func (sf *StructField) TagSettingsSet(key, val string) {
	sf.tagSettingsLock.Lock()
	defer sf.tagSettingsLock.Unlock()
	sf.TagSettings[key] = val
}

// TagSettingsGet returns a tag from the tag settings
func (sf *StructField) TagSettingsGet(key string) (string, bool) {
	sf.tagSettingsLock.RLock()
	defer sf.tagSettingsLock.RUnlock()
	val, ok := sf.TagSettings[key]
	return val, ok
}

// TagSettingsDelete deletes a tag
func (sf *StructField) TagSettingsDelete(key string) {
	sf.tagSettingsLock.Lock()
	defer sf.tagSettingsLock.Unlock()
	delete(sf.TagSettings, key)
}

func (sf *StructField) clone() *StructField {
	clone := &StructField{
		DBName:          sf.DBName,
		Name:            sf.Name,
		Names:           sf.Names,
		IsPrimaryKey:    sf.IsPrimaryKey,
		IsNormal:        sf.IsNormal,
		IsIgnored:       sf.IsIgnored,
		IsScanner:       sf.IsScanner,
		HasDefaultValue: sf.HasDefaultValue,
		Tag:             sf.Tag,
		TagSettings:     map[string]string{},
		Struct:          sf.Struct,
		IsForeignKey:    sf.IsForeignKey,
	}

	if sf.Relationship != nil {
		relationship := *sf.Relationship
		clone.Relationship = &relationship
	}

	// copy the struct field tagSettings, they should be read-locked while they are copied
	sf.tagSettingsLock.Lock()
	defer sf.tagSettingsLock.Unlock()
	for key, value := range sf.TagSettings {
		clone.TagSettings[key] = value
	}

	return clone
}

// Relationship described the relationship between models
// 描述结构体之前的关联关系
type Relationship struct {
	Kind                         string // 关联关系 一对多、多对多、一对一
	PolymorphicType              string // 多态类型表名
	PolymorphicDBName            string
	PolymorphicValue             string
	ForeignFieldNames            []string // 外键字段
	ForeignDBNames               []string // 外键对应的数据库表
	AssociationForeignFieldNames []string
	AssociationForeignDBNames    []string
	JoinTableHandler             JoinTableHandlerInterface
}

func getForeignField(column string, fields []*StructField) *StructField {
	for _, field := range fields {
		if field.Name == column || field.DBName == column || field.DBName == ToColumnName(column) {
			return field
		}
	}
	return nil
}

// GetModelStruct get value's model struct, relationships based on struct and tag definition
// 获取模型的Struct信息及关联关系，主要是通过Struct中的tag标识获取
func (scope *Scope) GetModelStruct() *ModelStruct {
	var modelStruct ModelStruct
	// Scope value can't be nil
	if scope.Value == nil {
		return &modelStruct
	}

	// 反射结构体
	reflectType := reflect.ValueOf(scope.Value).Type()
	for reflectType.Kind() == reflect.Slice || reflectType.Kind() == reflect.Ptr {
		reflectType = reflectType.Elem()
	}

	// Scope value need to be a struct
	if reflectType.Kind() != reflect.Struct {
		return &modelStruct
	}

	// Get Cached model struct  从缓存中获取模型
	if value, ok := modelStructsMap.Load(reflectType); ok && value != nil {
		return value.(*ModelStruct)
	}

	modelStruct.ModelType = reflectType //struct 模型类型

	// Get all fields  遍历Struct 中的字段
	for i := 0; i < reflectType.NumField(); i++ {
		if fieldStruct := reflectType.Field(i); ast.IsExported(fieldStruct.Name) {
			field := &StructField{
				Struct:      fieldStruct,
				Name:        fieldStruct.Name,
				Names:       []string{fieldStruct.Name},
				Tag:         fieldStruct.Tag,
				TagSettings: parseTagSetting(fieldStruct.Tag),
			}

			// is ignored field   - 忽略
			if _, ok := field.TagSettingsGet("-"); ok {
				field.IsIgnored = true
			} else {
				// PRIMARY_KEY
				if _, ok := field.TagSettingsGet("PRIMARY_KEY"); ok {
					field.IsPrimaryKey = true
					modelStruct.PrimaryFields = append(modelStruct.PrimaryFields, field)
				}
				// DEFAULT 是否有默认值
				if _, ok := field.TagSettingsGet("DEFAULT"); ok {
					field.HasDefaultValue = true
				}
				// AUTO_INCREMENT 自增 并且不是主键
				if _, ok := field.TagSettingsGet("AUTO_INCREMENT"); ok && !field.IsPrimaryKey {
					field.HasDefaultValue = true
				}

				indirectType := fieldStruct.Type
				for indirectType.Kind() == reflect.Ptr { // 指针类型
					indirectType = indirectType.Elem()
				}

				fieldValue := reflect.New(indirectType).Interface()
				if _, isScanner := fieldValue.(sql.Scanner); isScanner {
					// SQL中可被扫描对象
					// is scanner
					field.IsScanner, field.IsNormal = true, true
					if indirectType.Kind() == reflect.Struct {
						for i := 0; i < indirectType.NumField(); i++ {
							for key, value := range parseTagSetting(indirectType.Field(i).Tag) {
								if _, ok := field.TagSettingsGet(key); !ok {
									field.TagSettingsSet(key, value)
								}
							}
						}
					}
				} else if _, isTime := fieldValue.(*time.Time); isTime {
					// is time 时间类型
					field.IsNormal = true
				} else if _, ok := field.TagSettingsGet("EMBEDDED"); ok || fieldStruct.Anonymous {
					// is embedded struct  嵌套结构体类型
					for _, subField := range scope.New(fieldValue).GetModelStruct().StructFields {
						// 递归调用
						subField = subField.clone()
						subField.Names = append([]string{fieldStruct.Name}, subField.Names...)
						if prefix, ok := field.TagSettingsGet("EMBEDDED_PREFIX"); ok {
							subField.DBName = prefix + subField.DBName
						}

						if subField.IsPrimaryKey {
							if _, ok := subField.TagSettingsGet("PRIMARY_KEY"); ok {
								modelStruct.PrimaryFields = append(modelStruct.PrimaryFields, subField)
							} else {
								subField.IsPrimaryKey = false
							}
						}

						if subField.Relationship != nil && subField.Relationship.JoinTableHandler != nil {
							if joinTableHandler, ok := subField.Relationship.JoinTableHandler.(*JoinTableHandler); ok {
								newJoinTableHandler := &JoinTableHandler{}
								newJoinTableHandler.Setup(subField.Relationship, joinTableHandler.TableName, reflectType, joinTableHandler.Destination.ModelType)
								subField.Relationship.JoinTableHandler = newJoinTableHandler
							}
						}

						modelStruct.StructFields = append(modelStruct.StructFields, subField)
					}
					continue
				} else {
					// build relationships 建立关联关系
					switch indirectType.Kind() {
					case reflect.Slice: //切片
						defer func(field *StructField) {
							var (
								relationship           = &Relationship{}
								toScope                = scope.New(reflect.New(field.Struct.Type).Interface())
								foreignKeys            []string
								associationForeignKeys []string
								elemType               = field.Struct.Type
							)

							if foreignKey, _ := field.TagSettingsGet("FOREIGNKEY"); foreignKey != "" {
								foreignKeys = strings.Split(foreignKey, ",")
							}

							if foreignKey, _ := field.TagSettingsGet("ASSOCIATION_FOREIGNKEY"); foreignKey != "" {
								associationForeignKeys = strings.Split(foreignKey, ",")
							} else if foreignKey, _ := field.TagSettingsGet("ASSOCIATIONFOREIGNKEY"); foreignKey != "" {
								associationForeignKeys = strings.Split(foreignKey, ",")
							}

							for elemType.Kind() == reflect.Slice || elemType.Kind() == reflect.Ptr {
								elemType = elemType.Elem()
							}
							//many_to_many
							if elemType.Kind() == reflect.Struct {
								if many2many, _ := field.TagSettingsGet("MANY2MANY"); many2many != "" {
									relationship.Kind = "many_to_many"

									{ // Foreign Keys for Source
										joinTableDBNames := []string{}

										if foreignKey, _ := field.TagSettingsGet("JOINTABLE_FOREIGNKEY"); foreignKey != "" {
											joinTableDBNames = strings.Split(foreignKey, ",")
										}

										// if no foreign keys defined with tag
										if len(foreignKeys) == 0 {
											for _, field := range modelStruct.PrimaryFields {
												foreignKeys = append(foreignKeys, field.DBName)
											}
										}

										for idx, foreignKey := range foreignKeys {
											if foreignField := getForeignField(foreignKey, modelStruct.StructFields); foreignField != nil {
												// source foreign keys (db names)
												relationship.ForeignFieldNames = append(relationship.ForeignFieldNames, foreignField.DBName)

												// setup join table foreign keys for source
												if len(joinTableDBNames) > idx {
													// if defined join table's foreign key
													relationship.ForeignDBNames = append(relationship.ForeignDBNames, joinTableDBNames[idx])
												} else {
													defaultJointableForeignKey := ToColumnName(reflectType.Name()) + "_" + foreignField.DBName
													relationship.ForeignDBNames = append(relationship.ForeignDBNames, defaultJointableForeignKey)
												}
											}
										}
									}

									{ // Foreign Keys for Association (Destination)
										associationJoinTableDBNames := []string{}

										if foreignKey, _ := field.TagSettingsGet("ASSOCIATION_JOINTABLE_FOREIGNKEY"); foreignKey != "" {
											associationJoinTableDBNames = strings.Split(foreignKey, ",")
										}

										// if no association foreign keys defined with tag
										if len(associationForeignKeys) == 0 {
											for _, field := range toScope.PrimaryFields() {
												associationForeignKeys = append(associationForeignKeys, field.DBName)
											}
										}

										for idx, name := range associationForeignKeys {
											if field, ok := toScope.FieldByName(name); ok {
												// association foreign keys (db names)
												relationship.AssociationForeignFieldNames = append(relationship.AssociationForeignFieldNames, field.DBName)

												// setup join table foreign keys for association
												if len(associationJoinTableDBNames) > idx {
													relationship.AssociationForeignDBNames = append(relationship.AssociationForeignDBNames, associationJoinTableDBNames[idx])
												} else {
													// join table foreign keys for association
													joinTableDBName := ToColumnName(elemType.Name()) + "_" + field.DBName
													relationship.AssociationForeignDBNames = append(relationship.AssociationForeignDBNames, joinTableDBName)
												}
											}
										}
									}

									joinTableHandler := JoinTableHandler{}
									joinTableHandler.Setup(relationship, ToTableName(many2many), reflectType, elemType)
									relationship.JoinTableHandler = &joinTableHandler
									field.Relationship = relationship
								} else {
									// User has many comments, associationType is User, comment use UserID as foreign key
									var associationType = reflectType.Name()
									var toFields = toScope.GetStructFields()
									relationship.Kind = "has_many"

									if polymorphic, _ := field.TagSettingsGet("POLYMORPHIC"); polymorphic != "" {
										// Dog has many toys, tag polymorphic is Owner, then associationType is Owner
										// Toy use OwnerID, OwnerType ('dogs') as foreign key
										if polymorphicType := getForeignField(polymorphic+"Type", toFields); polymorphicType != nil {
											associationType = polymorphic
											relationship.PolymorphicType = polymorphicType.Name
											relationship.PolymorphicDBName = polymorphicType.DBName
											// if Dog has multiple set of toys set name of the set (instead of default 'dogs')
											if value, ok := field.TagSettingsGet("POLYMORPHIC_VALUE"); ok {
												relationship.PolymorphicValue = value
											} else {
												relationship.PolymorphicValue = scope.TableName()
											}
											polymorphicType.IsForeignKey = true
										}
									}

									// if no foreign keys defined with tag
									if len(foreignKeys) == 0 {
										// if no association foreign keys defined with tag
										if len(associationForeignKeys) == 0 {
											for _, field := range modelStruct.PrimaryFields {
												foreignKeys = append(foreignKeys, associationType+field.Name)
												associationForeignKeys = append(associationForeignKeys, field.Name)
											}
										} else {
											// generate foreign keys from defined association foreign keys
											for _, scopeFieldName := range associationForeignKeys {
												if foreignField := getForeignField(scopeFieldName, modelStruct.StructFields); foreignField != nil {
													foreignKeys = append(foreignKeys, associationType+foreignField.Name)
													associationForeignKeys = append(associationForeignKeys, foreignField.Name)
												}
											}
										}
									} else {
										// generate association foreign keys from foreign keys
										if len(associationForeignKeys) == 0 {
											for _, foreignKey := range foreignKeys {
												if strings.HasPrefix(foreignKey, associationType) {
													associationForeignKey := strings.TrimPrefix(foreignKey, associationType)
													if foreignField := getForeignField(associationForeignKey, modelStruct.StructFields); foreignField != nil {
														associationForeignKeys = append(associationForeignKeys, associationForeignKey)
													}
												}
											}
											if len(associationForeignKeys) == 0 && len(foreignKeys) == 1 {
												associationForeignKeys = []string{scope.PrimaryKey()}
											}
										} else if len(foreignKeys) != len(associationForeignKeys) {
											scope.Err(errors.New("invalid foreign keys, should have same length"))
											return
										}
									}

									for idx, foreignKey := range foreignKeys {
										if foreignField := getForeignField(foreignKey, toFields); foreignField != nil {
											if associationField := getForeignField(associationForeignKeys[idx], modelStruct.StructFields); associationField != nil {
												// source foreign keys
												foreignField.IsForeignKey = true
												relationship.AssociationForeignFieldNames = append(relationship.AssociationForeignFieldNames, associationField.Name)
												relationship.AssociationForeignDBNames = append(relationship.AssociationForeignDBNames, associationField.DBName)

												// association foreign keys
												relationship.ForeignFieldNames = append(relationship.ForeignFieldNames, foreignField.Name)
												relationship.ForeignDBNames = append(relationship.ForeignDBNames, foreignField.DBName)
											}
										}
									}

									if len(relationship.ForeignFieldNames) != 0 {
										field.Relationship = relationship
									}
								}
							} else {
								field.IsNormal = true
							}
						}(field)
					case reflect.Struct: // 结构体
						defer func(field *StructField) {
							var (
								// user has one profile, associationType is User, profile use UserID as foreign key
								// user belongs to profile, associationType is Profile, user use ProfileID as foreign key
								associationType           = reflectType.Name()
								relationship              = &Relationship{}
								toScope                   = scope.New(reflect.New(field.Struct.Type).Interface())
								toFields                  = toScope.GetStructFields()
								tagForeignKeys            []string
								tagAssociationForeignKeys []string
							)

							if foreignKey, _ := field.TagSettingsGet("FOREIGNKEY"); foreignKey != "" {
								tagForeignKeys = strings.Split(foreignKey, ",")
							}

							if foreignKey, _ := field.TagSettingsGet("ASSOCIATION_FOREIGNKEY"); foreignKey != "" {
								tagAssociationForeignKeys = strings.Split(foreignKey, ",")
							} else if foreignKey, _ := field.TagSettingsGet("ASSOCIATIONFOREIGNKEY"); foreignKey != "" {
								tagAssociationForeignKeys = strings.Split(foreignKey, ",")
							}

							if polymorphic, _ := field.TagSettingsGet("POLYMORPHIC"); polymorphic != "" {
								// Cat has one toy, tag polymorphic is Owner, then associationType is Owner
								// Toy use OwnerID, OwnerType ('cats') as foreign key
								if polymorphicType := getForeignField(polymorphic+"Type", toFields); polymorphicType != nil {
									associationType = polymorphic
									relationship.PolymorphicType = polymorphicType.Name
									relationship.PolymorphicDBName = polymorphicType.DBName
									// if Cat has several different types of toys set name for each (instead of default 'cats')
									if value, ok := field.TagSettingsGet("POLYMORPHIC_VALUE"); ok {
										relationship.PolymorphicValue = value
									} else {
										relationship.PolymorphicValue = scope.TableName()
									}
									polymorphicType.IsForeignKey = true
								}
							}

							// Has One
							{
								var foreignKeys = tagForeignKeys
								var associationForeignKeys = tagAssociationForeignKeys
								// if no foreign keys defined with tag
								if len(foreignKeys) == 0 {
									// if no association foreign keys defined with tag
									if len(associationForeignKeys) == 0 {
										for _, primaryField := range modelStruct.PrimaryFields {
											foreignKeys = append(foreignKeys, associationType+primaryField.Name)
											associationForeignKeys = append(associationForeignKeys, primaryField.Name)
										}
									} else {
										// generate foreign keys form association foreign keys
										for _, associationForeignKey := range tagAssociationForeignKeys {
											if foreignField := getForeignField(associationForeignKey, modelStruct.StructFields); foreignField != nil {
												foreignKeys = append(foreignKeys, associationType+foreignField.Name)
												associationForeignKeys = append(associationForeignKeys, foreignField.Name)
											}
										}
									}
								} else {
									// generate association foreign keys from foreign keys
									if len(associationForeignKeys) == 0 {
										for _, foreignKey := range foreignKeys {
											if strings.HasPrefix(foreignKey, associationType) {
												associationForeignKey := strings.TrimPrefix(foreignKey, associationType)
												if foreignField := getForeignField(associationForeignKey, modelStruct.StructFields); foreignField != nil {
													associationForeignKeys = append(associationForeignKeys, associationForeignKey)
												}
											}
										}
										if len(associationForeignKeys) == 0 && len(foreignKeys) == 1 {
											associationForeignKeys = []string{scope.PrimaryKey()}
										}
									} else if len(foreignKeys) != len(associationForeignKeys) {
										scope.Err(errors.New("invalid foreign keys, should have same length"))
										return
									}
								}

								for idx, foreignKey := range foreignKeys {
									if foreignField := getForeignField(foreignKey, toFields); foreignField != nil {
										if scopeField := getForeignField(associationForeignKeys[idx], modelStruct.StructFields); scopeField != nil {
											foreignField.IsForeignKey = true
											// source foreign keys
											relationship.AssociationForeignFieldNames = append(relationship.AssociationForeignFieldNames, scopeField.Name)
											relationship.AssociationForeignDBNames = append(relationship.AssociationForeignDBNames, scopeField.DBName)

											// association foreign keys
											relationship.ForeignFieldNames = append(relationship.ForeignFieldNames, foreignField.Name)
											relationship.ForeignDBNames = append(relationship.ForeignDBNames, foreignField.DBName)
										}
									}
								}
							}

							if len(relationship.ForeignFieldNames) != 0 {
								relationship.Kind = "has_one"
								field.Relationship = relationship
							} else {
								var foreignKeys = tagForeignKeys
								var associationForeignKeys = tagAssociationForeignKeys

								if len(foreignKeys) == 0 {
									// generate foreign keys & association foreign keys
									if len(associationForeignKeys) == 0 {
										for _, primaryField := range toScope.PrimaryFields() {
											foreignKeys = append(foreignKeys, field.Name+primaryField.Name)
											associationForeignKeys = append(associationForeignKeys, primaryField.Name)
										}
									} else {
										// generate foreign keys with association foreign keys
										for _, associationForeignKey := range associationForeignKeys {
											if foreignField := getForeignField(associationForeignKey, toFields); foreignField != nil {
												foreignKeys = append(foreignKeys, field.Name+foreignField.Name)
												associationForeignKeys = append(associationForeignKeys, foreignField.Name)
											}
										}
									}
								} else {
									// generate foreign keys & association foreign keys
									if len(associationForeignKeys) == 0 {
										for _, foreignKey := range foreignKeys {
											if strings.HasPrefix(foreignKey, field.Name) {
												associationForeignKey := strings.TrimPrefix(foreignKey, field.Name)
												if foreignField := getForeignField(associationForeignKey, toFields); foreignField != nil {
													associationForeignKeys = append(associationForeignKeys, associationForeignKey)
												}
											}
										}
										if len(associationForeignKeys) == 0 && len(foreignKeys) == 1 {
											associationForeignKeys = []string{toScope.PrimaryKey()}
										}
									} else if len(foreignKeys) != len(associationForeignKeys) {
										scope.Err(errors.New("invalid foreign keys, should have same length"))
										return
									}
								}

								for idx, foreignKey := range foreignKeys {
									if foreignField := getForeignField(foreignKey, modelStruct.StructFields); foreignField != nil {
										if associationField := getForeignField(associationForeignKeys[idx], toFields); associationField != nil {
											foreignField.IsForeignKey = true

											// association foreign keys
											relationship.AssociationForeignFieldNames = append(relationship.AssociationForeignFieldNames, associationField.Name)
											relationship.AssociationForeignDBNames = append(relationship.AssociationForeignDBNames, associationField.DBName)

											// source foreign keys
											relationship.ForeignFieldNames = append(relationship.ForeignFieldNames, foreignField.Name)
											relationship.ForeignDBNames = append(relationship.ForeignDBNames, foreignField.DBName)
										}
									}
								}

								if len(relationship.ForeignFieldNames) != 0 {
									relationship.Kind = "belongs_to"
									field.Relationship = relationship
								}
							}
						}(field)
					default: //普通类型
						field.IsNormal = true
					}
				}
			}

			// Even it is ignored, also possible to decode db value into the field
			if value, ok := field.TagSettingsGet("COLUMN"); ok {
				field.DBName = value
			} else {
				field.DBName = ToColumnName(fieldStruct.Name)
			}

			modelStruct.StructFields = append(modelStruct.StructFields, field)
		}
	}

	if len(modelStruct.PrimaryFields) == 0 {
		if field := getForeignField("id", modelStruct.StructFields); field != nil {
			field.IsPrimaryKey = true
			modelStruct.PrimaryFields = append(modelStruct.PrimaryFields, field)
		}
	}

	modelStructsMap.Store(reflectType, &modelStruct)

	return &modelStruct
}

// GetStructFields get model's field structs
func (scope *Scope) GetStructFields() (fields []*StructField) {
	return scope.GetModelStruct().StructFields
}

// 解析Tag 配置  带有sql或者gorm的效果一样，同一个字段多个属性的用分号”;“隔开
func parseTagSetting(tags reflect.StructTag) map[string]string {
	setting := map[string]string{}
	for _, str := range []string{tags.Get("sql"), tags.Get("gorm")} {
		tags := strings.Split(str, ";")
		for _, value := range tags {
			v := strings.Split(value, ":")
			k := strings.TrimSpace(strings.ToUpper(v[0])) //统一转换大写
			if len(v) >= 2 {
				setting[k] = strings.Join(v[1:], ":")
			} else {
				setting[k] = k
			}
		}
	}
	return setting
}
