package gorm

import "log"

// DefaultCallback default callbacks defined by gorm
var DefaultCallback = &Callback{}

// Callback is a struct that contains all CRUD callbacks
//   Field `creates` contains callbacks will be call when creating object
//   Field `updates` contains callbacks will be call when updating object
//   Field `deletes` contains callbacks will be call when deleting object
//   Field `queries` contains callbacks will be call when querying object with query methods like Find, First, Related, Association...
//   Field `rowQueries` contains callbacks will be call when querying object with Row, Rows...
//   Field `processors` contains all callback processors, will be used to generate above callbacks in order
// Callback 实现数据库CRUD操作，及其结果映射
type Callback struct {
	creates    []*func(scope *Scope) // INSERT操作
	updates    []*func(scope *Scope) // UPDATE操作
	deletes    []*func(scope *Scope) // DELETE操作
	queries    []*func(scope *Scope) // QUERY操作
	rowQueries []*func(scope *Scope)
	processors []*CallbackProcessor
}

// CallbackProcessor contains callback informations
type CallbackProcessor struct {
	name      string              // current callback's name
	before    string              // register current callback before a callback
	after     string              // register current callback after a callback
	replace   bool                // replace callbacks with same name
	remove    bool                // delete callbacks with same name
	kind      string              // callback type: create, update, delete, query, row_query
	processor *func(scope *Scope) // callback handler
	parent    *Callback
}

func (c *Callback) clone() *Callback {
	return &Callback{
		creates:    c.creates,
		updates:    c.updates,
		deletes:    c.deletes,
		queries:    c.queries,
		rowQueries: c.rowQueries,
		processors: c.processors,
	}
}

// Create could be used to register callbacks for creating object
//     db.Callback().Create().After("gorm:create").Register("plugin:run_after_create", func(*Scope) {
//       // business logic
//       ...
//
//       // set error if some thing wrong happened, will rollback the creating
//       scope.Err(errors.New("error"))
//     })
// 创建SQL类型的处理器
func (c *Callback) Create() *CallbackProcessor {
	return &CallbackProcessor{kind: "create", parent: c}
}

// Update could be used to register callbacks for updating object, refer `Create` for usage
func (c *Callback) Update() *CallbackProcessor {
	return &CallbackProcessor{kind: "update", parent: c}
}

// Delete could be used to register callbacks for deleting object, refer `Create` for usage
func (c *Callback) Delete() *CallbackProcessor {
	return &CallbackProcessor{kind: "delete", parent: c}
}

// Query could be used to register callbacks for querying objects with query methods like `Find`, `First`, `Related`, `Association`...
// Refer `Create` for usage
func (c *Callback) Query() *CallbackProcessor {
	return &CallbackProcessor{kind: "query", parent: c}
}

// RowQuery could be used to register callbacks for querying objects with `Row`, `Rows`, refer `Create` for usage
func (c *Callback) RowQuery() *CallbackProcessor {
	return &CallbackProcessor{kind: "row_query", parent: c}
}

// After insert a new callback after callback `callbackName`, refer `Callbacks.Create`
func (cp *CallbackProcessor) After(callbackName string) *CallbackProcessor {
	cp.after = callbackName
	return cp
}

// Before insert a new callback before callback `callbackName`, refer `Callbacks.Create`
func (cp *CallbackProcessor) Before(callbackName string) *CallbackProcessor {
	cp.before = callbackName
	return cp
}

// Register a new callback, refer `Callbacks.Create`
func (cp *CallbackProcessor) Register(callbackName string, callback func(scope *Scope)) {
	if cp.kind == "row_query" {
		if cp.before == "" && cp.after == "" && callbackName != "gorm:row_query" {
			log.Printf("Registing RowQuery callback %v without specify order with Before(), After(), applying Before('gorm:row_query') by default for compatibility...\n", callbackName)
			cp.before = "gorm:row_query"
		}
	}

	cp.name = callbackName
	cp.processor = &callback
	cp.parent.processors = append(cp.parent.processors, cp)
	cp.parent.reorder() //重新排序
}

// Remove a registered callback
//     db.Callback().Create().Remove("gorm:update_time_stamp_when_create")
func (cp *CallbackProcessor) Remove(callbackName string) {
	log.Printf("[info] removing callback `%v` from %v\n", callbackName, fileWithLineNum())
	cp.name = callbackName
	cp.remove = true
	cp.parent.processors = append(cp.parent.processors, cp)
	cp.parent.reorder()
}

// Replace a registered callback with new callback
//     db.Callback().Create().Replace("gorm:update_time_stamp_when_create", func(*Scope) {
//		   scope.SetColumn("Created", now)
//		   scope.SetColumn("Updated", now)
//     })
func (cp *CallbackProcessor) Replace(callbackName string, callback func(scope *Scope)) {
	log.Printf("[info] replacing callback `%v` from %v\n", callbackName, fileWithLineNum())
	cp.name = callbackName
	cp.processor = &callback
	cp.replace = true
	cp.parent.processors = append(cp.parent.processors, cp)
	cp.parent.reorder()
}

// Get registered callback
//    db.Callback().Create().Get("gorm:create")
func (cp *CallbackProcessor) Get(callbackName string) (callback func(scope *Scope)) {
	for _, p := range cp.parent.processors {
		if p.name == callbackName && p.kind == cp.kind && !cp.remove {
			return *p.processor
		}
	}
	return nil
}

// getRIndex get right index from string slice
func getRIndex(strs []string, str string) int {
	for i := len(strs) - 1; i >= 0; i-- {
		if strs[i] == str {
			return i
		}
	}
	return -1
}

// sortProcessors sort callback processors based on its before, after, remove, replace
// 对处理器进行排序,为后面处理流程准备
// 排序规则：
// 1、若没有指定before，after 则按照注册顺序进行排序
// 2、若指定了before，after规则，则按照规则排序
func sortProcessors(cps []*CallbackProcessor) []*func(scope *Scope) {
	var (
		allNames, sortedNames []string
		sortCallbackProcessor func(c *CallbackProcessor)
	)

	for _, cp := range cps {
		// show warning message the callback name already exists  相同callback处理器名称的waring提示
		if index := getRIndex(allNames, cp.name); index > -1 && !cp.replace && !cp.remove {
			log.Printf("[warning] duplicated callback `%v` from %v\n", cp.name, fileWithLineNum())
		}
		allNames = append(allNames, cp.name)
	}

	sortCallbackProcessor = func(c *CallbackProcessor) {
		if getRIndex(sortedNames, c.name) == -1 { // if not sorted 如果没有排序，则进行排序，排序好的不需要处理
			if c.before != "" { // if defined before callback
				if index := getRIndex(sortedNames, c.before); index != -1 {
					// if before callback already sorted, append current callback just after it
					sortedNames = append(sortedNames[:index], append([]string{c.name}, sortedNames[index:]...)...)
				} else if index := getRIndex(allNames, c.before); index != -1 {
					// if before callback exists but haven't sorted, append current callback to last
					sortedNames = append(sortedNames, c.name)
					sortCallbackProcessor(cps[index]) // 递归调用
				}
			}

			if c.after != "" { // if defined after callback
				if index := getRIndex(sortedNames, c.after); index != -1 {
					// if after callback already sorted, append current callback just before it
					sortedNames = append(sortedNames[:index+1], append([]string{c.name}, sortedNames[index+1:]...)...)
				} else if index := getRIndex(allNames, c.after); index != -1 {
					// if after callback exists but haven't sorted
					cp := cps[index]
					// set after callback's before callback to current callback
					if cp.before == "" {
						cp.before = c.name
					}
					sortCallbackProcessor(cp)
				}
			}

			// if current callback haven't been sorted, append it to last
			// 若没有befor after 则进行自然注册的顺序排序
			if getRIndex(sortedNames, c.name) == -1 {
				sortedNames = append(sortedNames, c.name)
			}
		}
	}

	for _, cp := range cps {
		sortCallbackProcessor(cp)
	}

	// 根据sort的name排序后的名字进行排序
	var sortedFuncs []*func(scope *Scope)
	for _, name := range sortedNames {
		if index := getRIndex(allNames, name); !cps[index].remove {
			sortedFuncs = append(sortedFuncs, cps[index].processor)
		}
	}

	return sortedFuncs
}

// reorder all registered processors, and reset CRUD callbacks   重新分配
func (c *Callback) reorder() {
	var creates, updates, deletes, queries, rowQueries []*CallbackProcessor

	for _, processor := range c.processors {
		if processor.name != "" {
			switch processor.kind {
			case "create":
				creates = append(creates, processor)
			case "update":
				updates = append(updates, processor)
			case "delete":
				deletes = append(deletes, processor)
			case "query":
				queries = append(queries, processor)
			case "row_query":
				rowQueries = append(rowQueries, processor)
			}
		}
	}

	c.creates = sortProcessors(creates)
	c.updates = sortProcessors(updates)
	c.deletes = sortProcessors(deletes)
	c.queries = sortProcessors(queries)
	c.rowQueries = sortProcessors(rowQueries)
}
