package gorm

import (
	"fmt"
)

//保存SQL条件
type search struct {
	db               *DB
	whereConditions  []map[string]interface{} // where 条件语句
	orConditions     []map[string]interface{} // or 条件语句
	notConditions    []map[string]interface{} // not 条件语句
	havingConditions []map[string]interface{} // having 条件语句
	joinConditions   []map[string]interface{} // join 条件语句
	initAttrs        []interface{}            // 参数初始化，若没有查询到相关记录
	assignAttrs      []interface{}            // 参数返回，不管是否查询到相关记录
	selects          map[string]interface{}   // 指定要从数据库检索的字段
	omits            []string                 // 忽略指定字段的更新
	orders           []interface{}            // order 排序的字段
	preload          []searchPreload          // 预加载
	offset           interface{}              // offset偏移 分页查询
	limit            interface{}              // limit 限制记录条数
	group            string                   // group 分类
	tableName        string                   // 表名字
	raw              bool                     // 是否为原始SQL访问
	Unscoped         bool                     // 是否查询出所有记录，（包括delete_time 数据）
	ignoreOrderQuery bool                     // 是否忽略排序
}

type searchPreload struct {
	schema     string
	conditions []interface{}
}

func (s *search) clone() *search {
	clone := *s
	return &clone
}

// 添加到Where条件
func (s *search) Where(query interface{}, values ...interface{}) *search {
	s.whereConditions = append(s.whereConditions, map[string]interface{}{"query": query, "args": values})
	return s
}

func (s *search) Not(query interface{}, values ...interface{}) *search {
	s.notConditions = append(s.notConditions, map[string]interface{}{"query": query, "args": values})
	return s
}

func (s *search) Or(query interface{}, values ...interface{}) *search {
	s.orConditions = append(s.orConditions, map[string]interface{}{"query": query, "args": values})
	return s
}

func (s *search) Attrs(attrs ...interface{}) *search {
	s.initAttrs = append(s.initAttrs, toSearchableMap(attrs...))
	return s
}

func (s *search) Assign(attrs ...interface{}) *search {
	s.assignAttrs = append(s.assignAttrs, toSearchableMap(attrs...))
	return s
}

func (s *search) Order(value interface{}, reorder ...bool) *search {
	if len(reorder) > 0 && reorder[0] {
		s.orders = []interface{}{}
	}

	if value != nil && value != "" {
		s.orders = append(s.orders, value)
	}
	return s
}

func (s *search) Select(query interface{}, args ...interface{}) *search {
	s.selects = map[string]interface{}{"query": query, "args": args}
	return s
}

func (s *search) Omit(columns ...string) *search {
	s.omits = columns
	return s
}

func (s *search) Limit(limit interface{}) *search {
	s.limit = limit
	return s
}

func (s *search) Offset(offset interface{}) *search {
	s.offset = offset
	return s
}

func (s *search) Group(query string) *search {
	s.group = s.getInterfaceAsSQL(query)
	return s
}

func (s *search) Having(query interface{}, values ...interface{}) *search {
	if val, ok := query.(*expr); ok {
		s.havingConditions = append(s.havingConditions, map[string]interface{}{"query": val.expr, "args": val.args})
	} else {
		s.havingConditions = append(s.havingConditions, map[string]interface{}{"query": query, "args": values})
	}
	return s
}

func (s *search) Joins(query string, values ...interface{}) *search {
	s.joinConditions = append(s.joinConditions, map[string]interface{}{"query": query, "args": values})
	return s
}

func (s *search) Preload(schema string, values ...interface{}) *search {
	var preloads []searchPreload
	for _, preload := range s.preload {
		if preload.schema != schema {
			preloads = append(preloads, preload)
		}
	}
	preloads = append(preloads, searchPreload{schema, values})
	s.preload = preloads
	return s
}

func (s *search) Raw(b bool) *search {
	s.raw = b
	return s
}

func (s *search) unscoped() *search {
	s.Unscoped = true
	return s
}

func (s *search) Table(name string) *search {
	s.tableName = name
	return s
}

func (s *search) getInterfaceAsSQL(value interface{}) (str string) {
	switch value.(type) {
	case string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		str = fmt.Sprintf("%v", value)
	default:
		s.db.AddError(ErrInvalidSQL)
	}

	if str == "-1" {
		return ""
	}
	return
}
