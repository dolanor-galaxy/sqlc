package dolphin

import (
	"fmt"
	"log"

	pcast "github.com/pingcap/parser/ast"
	"github.com/pingcap/parser/opcode"
	driver "github.com/pingcap/parser/test_driver"
	"github.com/pingcap/parser/types"

	"github.com/kyleconroy/sqlc/internal/debug"
	"github.com/kyleconroy/sqlc/internal/sql/ast"
)

type cc struct {
	paramCount int
}

func (c *cc) convertAlterTableStmt(n *pcast.AlterTableStmt) ast.Node {
	alt := &ast.AlterTableStmt{
		Table: parseTableName(n.Table),
		Cmds:  &ast.List{},
	}
	for _, spec := range n.Specs {
		switch spec.Tp {
		case pcast.AlterTableAddColumns:
			for _, def := range spec.NewColumns {
				name := def.Name.String()
				alt.Cmds.Items = append(alt.Cmds.Items, &ast.AlterTableCmd{
					Name:    &name,
					Subtype: ast.AT_AddColumn,
					Def: &ast.ColumnDef{
						Colname:   def.Name.String(),
						TypeName:  &ast.TypeName{Name: types.TypeStr(def.Tp.Tp)},
						IsNotNull: isNotNull(def),
					},
				})
			}

		case pcast.AlterTableDropColumn:
			name := spec.OldColumnName.String()
			alt.Cmds.Items = append(alt.Cmds.Items, &ast.AlterTableCmd{
				Name:      &name,
				Subtype:   ast.AT_DropColumn,
				MissingOk: spec.IfExists,
			})

		case pcast.AlterTableChangeColumn:
			// 	spew.Dump("change column", spec)

		case pcast.AlterTableModifyColumn:
			for _, def := range spec.NewColumns {
				name := def.Name.String()
				alt.Cmds.Items = append(alt.Cmds.Items, &ast.AlterTableCmd{
					Name:    &name,
					Subtype: ast.AT_DropColumn,
				})
				alt.Cmds.Items = append(alt.Cmds.Items, &ast.AlterTableCmd{
					Name:    &name,
					Subtype: ast.AT_AddColumn,
					Def: &ast.ColumnDef{
						Colname:   def.Name.String(),
						TypeName:  &ast.TypeName{Name: types.TypeStr(def.Tp.Tp)},
						IsNotNull: isNotNull(def),
					},
				})
			}

		case pcast.AlterTableAlterColumn:
			// 	spew.Dump("alter column", spec)

		case pcast.AlterTableAddConstraint:
			// 	spew.Dump("add const", spec)

		case pcast.AlterTableRenameColumn:
			// TODO: Returning here may be incorrect if there are multiple specs
			oldName := spec.OldColumnName.String()
			newName := spec.NewColumnName.String()
			return &ast.RenameColumnStmt{
				Table:   parseTableName(n.Table),
				Col:     &ast.ColumnRef{Name: oldName},
				NewName: &newName,
			}

		case pcast.AlterTableRenameTable:
			// TODO: Returning here may be incorrect if there are multiple specs
			return &ast.RenameTableStmt{
				Table:   parseTableName(n.Table),
				NewName: &parseTableName(spec.NewTable).Name,
			}

		default:
			if debug.Active {
				fmt.Printf("dolphin.convert: Unknown alter table cmd %v\n", spec.Tp)
			}
			continue
		}
	}
	return alt
}

func (c *cc) convertAssignment(n *pcast.Assignment) *ast.ResTarget {
	name := n.Column.Name.String()
	return &ast.ResTarget{
		Name: &name,
		Val:  c.convert(n.Expr),
	}
}

func opToName(o opcode.Op) string {
	switch o {
	case opcode.EQ:
		return "="
	}
	return o.String()
}

func (c *cc) convertBinaryOperationExpr(n *pcast.BinaryOperationExpr) ast.Node {
	if n.Op == opcode.LogicAnd || n.Op == opcode.LogicOr {
		return &ast.BoolExpr{
			// TODO: Set op
			Args: &ast.List{
				Items: []ast.Node{
					c.convert(n.L),
					c.convert(n.R),
				},
			},
		}
	} else {
		return &ast.A_Expr{
			// TODO: Set kind
			Name: &ast.List{
				Items: []ast.Node{
					&ast.String{Str: opToName(n.Op)},
				},
			},
			Lexpr: c.convert(n.L),
			Rexpr: c.convert(n.R),
		}
	}
}

func (c *cc) convertCreateTableStmt(n *pcast.CreateTableStmt) ast.Node {
	create := &ast.CreateTableStmt{
		Name:        parseTableName(n.Table),
		IfNotExists: n.IfNotExists,
	}
	if n.ReferTable != nil {
		create.ReferTable = parseTableName(n.ReferTable)
	}
	for _, def := range n.Cols {
		var vals *ast.List
		if len(def.Tp.Elems) > 0 {
			vals = &ast.List{}
			for i := range def.Tp.Elems {
				vals.Items = append(vals.Items, &ast.String{
					Str: def.Tp.Elems[i],
				})
			}
		}
		comment := ""
		for _, opt := range def.Options {
			switch opt.Tp {
			case pcast.ColumnOptionComment:
				if value, ok := opt.Expr.(*driver.ValueExpr); ok {
					comment = value.GetString()
				}
			}
		}
		create.Cols = append(create.Cols, &ast.ColumnDef{
			Colname:   def.Name.String(),
			TypeName:  &ast.TypeName{Name: types.TypeStr(def.Tp.Tp)},
			IsNotNull: isNotNull(def),
			Comment:   comment,
			Vals:      vals,
		})
	}
	for _, opt := range n.Options {
		switch opt.Tp {
		case pcast.TableOptionComment:
			create.Comment = opt.StrValue
		}
	}
	return create
}

func (c *cc) convertColumnNameExpr(n *pcast.ColumnNameExpr) *ast.ColumnRef {
	return &ast.ColumnRef{
		Fields: &ast.List{
			Items: []ast.Node{
				&ast.String{Str: n.Name.Name.String()},
			},
		},
	}
}

func (c *cc) convertColumnNames(cols []*pcast.ColumnName) *ast.List {
	list := &ast.List{Items: []ast.Node{}}
	for i := range cols {
		name := cols[i].Name.String()
		list.Items = append(list.Items, &ast.ResTarget{
			Name: &name,
		})
	}
	return list
}

func (c *cc) convertDeleteStmt(n *pcast.DeleteStmt) *ast.DeleteStmt {
	rels := c.convertTableRefsClause(n.TableRefs)
	if len(rels.Items) != 1 {
		panic("expected one range var")
	}
	rel := rels.Items[0]
	rangeVar, ok := rel.(*ast.RangeVar)
	if !ok {
		panic("expected range var")
	}

	return &ast.DeleteStmt{
		Relation:      rangeVar,
		WhereClause:   c.convert(n.Where),
		ReturningList: &ast.List{},
	}
}

func (c *cc) convertDropTableStmt(n *pcast.DropTableStmt) ast.Node {
	// TODO: Remove once views are supported.
	if n.IsView {
		return &ast.TODO{}
	}
	drop := &ast.DropTableStmt{IfExists: n.IfExists}
	for _, name := range n.Tables {
		drop.Tables = append(drop.Tables, parseTableName(name))
	}
	return drop
}

func (c *cc) convertRenameTableStmt(n *pcast.RenameTableStmt) ast.Node {
	return &ast.RenameTableStmt{
		Table:   parseTableName(n.OldTable),
		NewName: &parseTableName(n.NewTable).Name,
	}
}

func (c *cc) convertExistsSubqueryExpr(n *pcast.ExistsSubqueryExpr) *ast.SubLink {
	sublink := &ast.SubLink{}
	if ss, ok := c.convert(n.Sel).(*ast.SelectStmt); ok {
		sublink.Subselect = ss
	}
	return sublink
}

func (c *cc) convertFieldList(n *pcast.FieldList) *ast.List {
	fields := make([]ast.Node, len(n.Fields))
	for i := range n.Fields {
		fields[i] = c.convertSelectField(n.Fields[i])
	}
	return &ast.List{Items: fields}
}

func (c *cc) convertFuncCallExpr(n *pcast.FuncCallExpr) *ast.FuncCall {
	schema := n.Schema.String()
	name := n.FnName.String()

	// TODO: Deprecate the usage of Funcname
	items := []ast.Node{}
	if schema != "" {
		items = append(items, &ast.String{Str: schema})
	}
	items = append(items, &ast.String{Str: name})

	fn := &ast.FuncCall{
		Args: &ast.List{},
		Func: &ast.FuncName{
			Schema: schema,
			Name:   name,
		},
		Funcname: &ast.List{
			Items: items,
		},
		Location: n.Offset,
	}
	for _, arg := range n.Args {
		fn.Args.Items = append(fn.Args.Items, c.convert(arg))
	}
	return fn
}

func (c *cc) convertInsertStmt(n *pcast.InsertStmt) *ast.InsertStmt {
	rels := c.convertTableRefsClause(n.Table)
	if len(rels.Items) != 1 {
		panic("expected one range var")
	}
	rel := rels.Items[0]
	rangeVar, ok := rel.(*ast.RangeVar)
	if !ok {
		panic("expected range var")
	}

	// debug.Dump(n)
	insert := &ast.InsertStmt{
		Relation:      rangeVar,
		Cols:          c.convertColumnNames(n.Columns),
		ReturningList: &ast.List{},
	}
	if ss, ok := c.convert(n.Select).(*ast.SelectStmt); ok {
		ss.ValuesLists = c.convertLists(n.Lists)
		insert.SelectStmt = ss
	} else {
		insert.SelectStmt = &ast.SelectStmt{
			FromClause:  &ast.List{},
			TargetList:  &ast.List{},
			ValuesLists: c.convertLists(n.Lists),
		}
	}
	return insert
}

func (c *cc) convertLists(lists [][]pcast.ExprNode) *ast.List {
	list := &ast.List{Items: []ast.Node{}}
	for _, exprs := range lists {
		inner := &ast.List{Items: []ast.Node{}}
		for _, expr := range exprs {
			inner.Items = append(inner.Items, c.convert(expr))
		}
		list.Items = append(list.Items, inner)
	}
	return list
}

func (c *cc) convertParamMarkerExpr(n *driver.ParamMarkerExpr) *ast.ParamRef {
	// Parameter numbers start at one
	c.paramCount += 1
	return &ast.ParamRef{
		Number:   c.paramCount,
		Location: n.Offset,
	}
}

func (c *cc) convertSelectField(n *pcast.SelectField) *ast.ResTarget {
	var val ast.Node
	if n.WildCard != nil {
		val = c.convertWildCardField(n.WildCard)
	} else {
		val = c.convert(n.Expr)
	}
	var name *string
	if n.AsName.O != "" {
		name = &n.AsName.O
	}
	return &ast.ResTarget{
		// TODO: Populate Indirection field
		Name:     name,
		Val:      val,
		Location: n.Offset,
	}
}

func (c *cc) convertSelectStmt(n *pcast.SelectStmt) *ast.SelectStmt {
	stmt := &ast.SelectStmt{
		TargetList:  c.convertFieldList(n.Fields),
		FromClause:  c.convertTableRefsClause(n.From),
		WhereClause: c.convert(n.Where),
	}
	if n.Limit != nil {
		stmt.LimitCount = c.convert(n.Limit.Count)
		stmt.LimitOffset = c.convert(n.Limit.Offset)
	}
	return stmt
}

func (c *cc) convertSubqueryExpr(n *pcast.SubqueryExpr) ast.Node {
	return c.convert(n.Query)
}

func (c *cc) convertTableRefsClause(n *pcast.TableRefsClause) *ast.List {
	var tables []ast.Node
	if n == nil {
		return &ast.List{Items: tables}
	}
	visit(n, func(n pcast.Node) {
		name, ok := n.(*pcast.TableName)
		if !ok {
			return
		}
		schema := name.Schema.String()
		rel := name.Name.String()
		tables = append(tables, &ast.RangeVar{
			Schemaname: &schema,
			Relname:    &rel,
		})
	})
	return &ast.List{Items: tables}
}

func (c *cc) convertUpdateStmt(n *pcast.UpdateStmt) *ast.UpdateStmt {
	// Relation
	rels := c.convertTableRefsClause(n.TableRefs)
	if len(rels.Items) != 1 {
		panic("expected one range var")
	}
	rel := rels.Items[0]
	rangeVar, ok := rel.(*ast.RangeVar)
	if !ok {
		panic("expected range var")
	}
	// TargetList
	list := &ast.List{}
	for _, a := range n.List {
		list.Items = append(list.Items, c.convertAssignment(a))
	}
	return &ast.UpdateStmt{
		Relation:      rangeVar,
		TargetList:    list,
		WhereClause:   c.convert(n.Where),
		FromClause:    &ast.List{},
		ReturningList: &ast.List{},
	}
}

func (c *cc) convertValueExpr(n *driver.ValueExpr) *ast.A_Const {
	return &ast.A_Const{
		Val: &ast.String{
			Str: n.Datum.GetString(),
		},
	}
}

func (c *cc) convertWildCardField(n *pcast.WildCardField) *ast.ColumnRef {
	items := []ast.Node{}
	if t := n.Table.String(); t != "" {
		items = append(items, &ast.String{Str: t})
	}
	items = append(items, &ast.A_Star{})

	return &ast.ColumnRef{
		Fields: &ast.List{
			Items: items,
		},
	}
}

func (c *cc) convertAdminStmt(n *pcast.AdminStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertAggregateFuncExpr(n *pcast.AggregateFuncExpr) *ast.FuncCall {
	fn := &ast.FuncCall{
		Func: &ast.FuncName{
			Name: n.F,
		},
		Funcname: &ast.List{
			Items: []ast.Node{
				&ast.String{
					Str: n.F,
				},
			},
		},
		Args:     &ast.List{},
		AggOrder: &ast.List{},
	}
	for _, a := range n.Args {
		if value, ok := a.(*driver.ValueExpr); ok {
			if value.GetInt64() == int64(1) {
				fn.AggStar = true
				continue
			}
		}
		fn.Args.Items = append(fn.Args.Items, c.convert(a))
	}
	if n.Distinct {
		fn.AggDistinct = true
	}
	return fn
}

func (c *cc) convertAlterDatabaseStmt(n *pcast.AlterDatabaseStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertAlterInstanceStmt(n *pcast.AlterInstanceStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertAlterTableSpec(n *pcast.AlterTableSpec) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertAlterUserStmt(n *pcast.AlterUserStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertAnalyzeTableStmt(n *pcast.AnalyzeTableStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertBRIEStmt(n *pcast.BRIEStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertBeginStmt(n *pcast.BeginStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertBetweenExpr(n *pcast.BetweenExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertBinlogStmt(n *pcast.BinlogStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertByItem(n *pcast.ByItem) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertCaseExpr(n *pcast.CaseExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertChangeStmt(n *pcast.ChangeStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertCleanupTableLockStmt(n *pcast.CleanupTableLockStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertColumnDef(n *pcast.ColumnDef) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertColumnName(n *pcast.ColumnName) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertColumnPosition(n *pcast.ColumnPosition) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertCommitStmt(n *pcast.CommitStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertCompareSubqueryExpr(n *pcast.CompareSubqueryExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertConstraint(n *pcast.Constraint) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertCreateBindingStmt(n *pcast.CreateBindingStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertCreateDatabaseStmt(n *pcast.CreateDatabaseStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertCreateIndexStmt(n *pcast.CreateIndexStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertCreateSequenceStmt(n *pcast.CreateSequenceStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertCreateStatisticsStmt(n *pcast.CreateStatisticsStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertCreateUserStmt(n *pcast.CreateUserStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertCreateViewStmt(n *pcast.CreateViewStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertDeallocateStmt(n *pcast.DeallocateStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertDefaultExpr(n *pcast.DefaultExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertDeleteTableList(n *pcast.DeleteTableList) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertDoStmt(n *pcast.DoStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertDropBindingStmt(n *pcast.DropBindingStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertDropDatabaseStmt(n *pcast.DropDatabaseStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertDropIndexStmt(n *pcast.DropIndexStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertDropSequenceStmt(n *pcast.DropSequenceStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertDropStatisticsStmt(n *pcast.DropStatisticsStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertDropStatsStmt(n *pcast.DropStatsStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertDropUserStmt(n *pcast.DropUserStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertExecuteStmt(n *pcast.ExecuteStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertExplainForStmt(n *pcast.ExplainForStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertExplainStmt(n *pcast.ExplainStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertFlashBackTableStmt(n *pcast.FlashBackTableStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertFlushStmt(n *pcast.FlushStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertFrameBound(n *pcast.FrameBound) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertFrameClause(n *pcast.FrameClause) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertFuncCastExpr(n *pcast.FuncCastExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertGetFormatSelectorExpr(n *pcast.GetFormatSelectorExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertGrantRoleStmt(n *pcast.GrantRoleStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertGrantStmt(n *pcast.GrantStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertGroupByClause(n *pcast.GroupByClause) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertHavingClause(n *pcast.HavingClause) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertIndexAdviseStmt(n *pcast.IndexAdviseStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertIndexLockAndAlgorithm(n *pcast.IndexLockAndAlgorithm) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertIndexPartSpecification(n *pcast.IndexPartSpecification) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertIsNullExpr(n *pcast.IsNullExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertIsTruthExpr(n *pcast.IsTruthExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertJoin(n *pcast.Join) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertKillStmt(n *pcast.KillStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertLimit(n *pcast.Limit) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertLoadDataStmt(n *pcast.LoadDataStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertLoadStatsStmt(n *pcast.LoadStatsStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertLockTablesStmt(n *pcast.LockTablesStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertMatchAgainst(n *pcast.MatchAgainst) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertMaxValueExpr(n *pcast.MaxValueExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertOnCondition(n *pcast.OnCondition) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertOnDeleteOpt(n *pcast.OnDeleteOpt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertOnUpdateOpt(n *pcast.OnUpdateOpt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertOrderByClause(n *pcast.OrderByClause) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertParenthesesExpr(n *pcast.ParenthesesExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertPartitionByClause(n *pcast.PartitionByClause) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertPatternInExpr(n *pcast.PatternInExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertPatternLikeExpr(n *pcast.PatternLikeExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertPatternRegexpExpr(n *pcast.PatternRegexpExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertPlacementSpec(n *pcast.PlacementSpec) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertPositionExpr(n *pcast.PositionExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertPrepareStmt(n *pcast.PrepareStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertPrivElem(n *pcast.PrivElem) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertRecoverTableStmt(n *pcast.RecoverTableStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertReferenceDef(n *pcast.ReferenceDef) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertRepairTableStmt(n *pcast.RepairTableStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertRevokeRoleStmt(n *pcast.RevokeRoleStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertRevokeStmt(n *pcast.RevokeStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertRollbackStmt(n *pcast.RollbackStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertRowExpr(n *pcast.RowExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertSetCollationExpr(n *pcast.SetCollationExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertSetConfigStmt(n *pcast.SetConfigStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertSetDefaultRoleStmt(n *pcast.SetDefaultRoleStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertSetOprSelectList(n *pcast.SetOprSelectList) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertSetOprStmt(n *pcast.SetOprStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertSetPwdStmt(n *pcast.SetPwdStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertSetRoleStmt(n *pcast.SetRoleStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertSetStmt(n *pcast.SetStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertShowStmt(n *pcast.ShowStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertShutdownStmt(n *pcast.ShutdownStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertSplitRegionStmt(n *pcast.SplitRegionStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertTableName(n *pcast.TableName) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertTableNameExpr(n *pcast.TableNameExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertTableOptimizerHint(n *pcast.TableOptimizerHint) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertTableSource(n *pcast.TableSource) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertTableToTable(n *pcast.TableToTable) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertTimeUnitExpr(n *pcast.TimeUnitExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertTraceStmt(n *pcast.TraceStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertTrimDirectionExpr(n *pcast.TrimDirectionExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertTruncateTableStmt(n *pcast.TruncateTableStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertUnaryOperationExpr(n *pcast.UnaryOperationExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertUnlockTablesStmt(n *pcast.UnlockTablesStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertUseStmt(n *pcast.UseStmt) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertValuesExpr(n *pcast.ValuesExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertVariableAssignment(n *pcast.VariableAssignment) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertVariableExpr(n *pcast.VariableExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertWhenClause(n *pcast.WhenClause) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertWindowFuncExpr(n *pcast.WindowFuncExpr) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convertWindowSpec(n *pcast.WindowSpec) ast.Node {
	return &ast.TODO{}
}

func (c *cc) convert(node pcast.Node) ast.Node {
	switch n := node.(type) {

	case *driver.ParamMarkerExpr:
		return c.convertParamMarkerExpr(n)

	case *driver.ValueExpr:
		return c.convertValueExpr(n)

	case *pcast.AdminStmt:
		return c.convertAdminStmt(n)

	case *pcast.AggregateFuncExpr:
		return c.convertAggregateFuncExpr(n)

	case *pcast.AlterDatabaseStmt:
		return c.convertAlterDatabaseStmt(n)

	case *pcast.AlterInstanceStmt:
		return c.convertAlterInstanceStmt(n)

	case *pcast.AlterTableSpec:
		return c.convertAlterTableSpec(n)

	case *pcast.AlterTableStmt:
		return c.convertAlterTableStmt(n)

	case *pcast.AlterUserStmt:
		return c.convertAlterUserStmt(n)

	case *pcast.AnalyzeTableStmt:
		return c.convertAnalyzeTableStmt(n)

	case *pcast.Assignment:
		return c.convertAssignment(n)

	case *pcast.BRIEStmt:
		return c.convertBRIEStmt(n)

	case *pcast.BeginStmt:
		return c.convertBeginStmt(n)

	case *pcast.BetweenExpr:
		return c.convertBetweenExpr(n)

	case *pcast.BinaryOperationExpr:
		return c.convertBinaryOperationExpr(n)

	case *pcast.BinlogStmt:
		return c.convertBinlogStmt(n)

	case *pcast.ByItem:
		return c.convertByItem(n)

	case *pcast.CaseExpr:
		return c.convertCaseExpr(n)

	case *pcast.ChangeStmt:
		return c.convertChangeStmt(n)

	case *pcast.CleanupTableLockStmt:
		return c.convertCleanupTableLockStmt(n)

	case *pcast.ColumnDef:
		return c.convertColumnDef(n)

	case *pcast.ColumnName:
		return c.convertColumnName(n)

	case *pcast.ColumnNameExpr:
		return c.convertColumnNameExpr(n)

	case *pcast.ColumnPosition:
		return c.convertColumnPosition(n)

	case *pcast.CommitStmt:
		return c.convertCommitStmt(n)

	case *pcast.CompareSubqueryExpr:
		return c.convertCompareSubqueryExpr(n)

	case *pcast.Constraint:
		return c.convertConstraint(n)

	case *pcast.CreateBindingStmt:
		return c.convertCreateBindingStmt(n)

	case *pcast.CreateDatabaseStmt:
		return c.convertCreateDatabaseStmt(n)

	case *pcast.CreateIndexStmt:
		return c.convertCreateIndexStmt(n)

	case *pcast.CreateSequenceStmt:
		return c.convertCreateSequenceStmt(n)

	case *pcast.CreateStatisticsStmt:
		return c.convertCreateStatisticsStmt(n)

	case *pcast.CreateTableStmt:
		return c.convertCreateTableStmt(n)

	case *pcast.CreateUserStmt:
		return c.convertCreateUserStmt(n)

	case *pcast.CreateViewStmt:
		return c.convertCreateViewStmt(n)

	case *pcast.DeallocateStmt:
		return c.convertDeallocateStmt(n)

	case *pcast.DefaultExpr:
		return c.convertDefaultExpr(n)

	case *pcast.DeleteStmt:
		return c.convertDeleteStmt(n)

	case *pcast.DeleteTableList:
		return c.convertDeleteTableList(n)

	case *pcast.DoStmt:
		return c.convertDoStmt(n)

	case *pcast.DropBindingStmt:
		return c.convertDropBindingStmt(n)

	case *pcast.DropDatabaseStmt:
		return c.convertDropDatabaseStmt(n)

	case *pcast.DropIndexStmt:
		return c.convertDropIndexStmt(n)

	case *pcast.DropSequenceStmt:
		return c.convertDropSequenceStmt(n)

	case *pcast.DropStatisticsStmt:
		return c.convertDropStatisticsStmt(n)

	case *pcast.DropStatsStmt:
		return c.convertDropStatsStmt(n)

	case *pcast.DropTableStmt:
		return c.convertDropTableStmt(n)

	case *pcast.DropUserStmt:
		return c.convertDropUserStmt(n)

	case *pcast.ExecuteStmt:
		return c.convertExecuteStmt(n)

	case *pcast.ExistsSubqueryExpr:
		return c.convertExistsSubqueryExpr(n)

	case *pcast.ExplainForStmt:
		return c.convertExplainForStmt(n)

	case *pcast.ExplainStmt:
		return c.convertExplainStmt(n)

	case *pcast.FieldList:
		return c.convertFieldList(n)

	case *pcast.FlashBackTableStmt:
		return c.convertFlashBackTableStmt(n)

	case *pcast.FlushStmt:
		return c.convertFlushStmt(n)

	case *pcast.FrameBound:
		return c.convertFrameBound(n)

	case *pcast.FrameClause:
		return c.convertFrameClause(n)

	case *pcast.FuncCallExpr:
		return c.convertFuncCallExpr(n)

	case *pcast.FuncCastExpr:
		return c.convertFuncCastExpr(n)

	case *pcast.GetFormatSelectorExpr:
		return c.convertGetFormatSelectorExpr(n)

	case *pcast.GrantRoleStmt:
		return c.convertGrantRoleStmt(n)

	case *pcast.GrantStmt:
		return c.convertGrantStmt(n)

	case *pcast.GroupByClause:
		return c.convertGroupByClause(n)

	case *pcast.HavingClause:
		return c.convertHavingClause(n)

	case *pcast.IndexAdviseStmt:
		return c.convertIndexAdviseStmt(n)

	case *pcast.IndexLockAndAlgorithm:
		return c.convertIndexLockAndAlgorithm(n)

	case *pcast.IndexPartSpecification:
		return c.convertIndexPartSpecification(n)

	case *pcast.InsertStmt:
		return c.convertInsertStmt(n)

	case *pcast.IsNullExpr:
		return c.convertIsNullExpr(n)

	case *pcast.IsTruthExpr:
		return c.convertIsTruthExpr(n)

	case *pcast.Join:
		return c.convertJoin(n)

	case *pcast.KillStmt:
		return c.convertKillStmt(n)

	case *pcast.Limit:
		return c.convertLimit(n)

	case *pcast.LoadDataStmt:
		return c.convertLoadDataStmt(n)

	case *pcast.LoadStatsStmt:
		return c.convertLoadStatsStmt(n)

	case *pcast.LockTablesStmt:
		return c.convertLockTablesStmt(n)

	case *pcast.MatchAgainst:
		return c.convertMatchAgainst(n)

	case *pcast.MaxValueExpr:
		return c.convertMaxValueExpr(n)

	case *pcast.OnCondition:
		return c.convertOnCondition(n)

	case *pcast.OnDeleteOpt:
		return c.convertOnDeleteOpt(n)

	case *pcast.OnUpdateOpt:
		return c.convertOnUpdateOpt(n)

	case *pcast.OrderByClause:
		return c.convertOrderByClause(n)

	case *pcast.ParenthesesExpr:
		return c.convertParenthesesExpr(n)

	case *pcast.PartitionByClause:
		return c.convertPartitionByClause(n)

	case *pcast.PatternInExpr:
		return c.convertPatternInExpr(n)

	case *pcast.PatternLikeExpr:
		return c.convertPatternLikeExpr(n)

	case *pcast.PatternRegexpExpr:
		return c.convertPatternRegexpExpr(n)

	case *pcast.PlacementSpec:
		return c.convertPlacementSpec(n)

	case *pcast.PositionExpr:
		return c.convertPositionExpr(n)

	case *pcast.PrepareStmt:
		return c.convertPrepareStmt(n)

	case *pcast.PrivElem:
		return c.convertPrivElem(n)

	case *pcast.RecoverTableStmt:
		return c.convertRecoverTableStmt(n)

	case *pcast.ReferenceDef:
		return c.convertReferenceDef(n)

	case *pcast.RenameTableStmt:
		return c.convertRenameTableStmt(n)

	case *pcast.RepairTableStmt:
		return c.convertRepairTableStmt(n)

	case *pcast.RevokeRoleStmt:
		return c.convertRevokeRoleStmt(n)

	case *pcast.RevokeStmt:
		return c.convertRevokeStmt(n)

	case *pcast.RollbackStmt:
		return c.convertRollbackStmt(n)

	case *pcast.RowExpr:
		return c.convertRowExpr(n)

	case *pcast.SelectField:
		return c.convertSelectField(n)

	case *pcast.SelectStmt:
		return c.convertSelectStmt(n)

	case *pcast.SetCollationExpr:
		return c.convertSetCollationExpr(n)

	case *pcast.SetConfigStmt:
		return c.convertSetConfigStmt(n)

	case *pcast.SetDefaultRoleStmt:
		return c.convertSetDefaultRoleStmt(n)

	case *pcast.SetOprSelectList:
		return c.convertSetOprSelectList(n)

	case *pcast.SetOprStmt:
		return c.convertSetOprStmt(n)

	case *pcast.SetPwdStmt:
		return c.convertSetPwdStmt(n)

	case *pcast.SetRoleStmt:
		return c.convertSetRoleStmt(n)

	case *pcast.SetStmt:
		return c.convertSetStmt(n)

	case *pcast.ShowStmt:
		return c.convertShowStmt(n)

	case *pcast.ShutdownStmt:
		return c.convertShutdownStmt(n)

	case *pcast.SplitRegionStmt:
		return c.convertSplitRegionStmt(n)

	case *pcast.SubqueryExpr:
		return c.convertSubqueryExpr(n)

	case *pcast.TableName:
		return c.convertTableName(n)

	case *pcast.TableNameExpr:
		return c.convertTableNameExpr(n)

	case *pcast.TableOptimizerHint:
		return c.convertTableOptimizerHint(n)

	case *pcast.TableRefsClause:
		return c.convertTableRefsClause(n)

	case *pcast.TableSource:
		return c.convertTableSource(n)

	case *pcast.TableToTable:
		return c.convertTableToTable(n)

	case *pcast.TimeUnitExpr:
		return c.convertTimeUnitExpr(n)

	case *pcast.TraceStmt:
		return c.convertTraceStmt(n)

	case *pcast.TrimDirectionExpr:
		return c.convertTrimDirectionExpr(n)

	case *pcast.TruncateTableStmt:
		return c.convertTruncateTableStmt(n)

	case *pcast.UnaryOperationExpr:
		return c.convertUnaryOperationExpr(n)

	case *pcast.UnlockTablesStmt:
		return c.convertUnlockTablesStmt(n)

	case *pcast.UpdateStmt:
		return c.convertUpdateStmt(n)

	case *pcast.UseStmt:
		return c.convertUseStmt(n)

	case *pcast.ValuesExpr:
		return c.convertValuesExpr(n)

	case *pcast.VariableAssignment:
		return c.convertVariableAssignment(n)

	case *pcast.VariableExpr:
		return c.convertVariableExpr(n)

	case *pcast.WhenClause:
		return c.convertWhenClause(n)

	case *pcast.WildCardField:
		return c.convertWildCardField(n)

	case *pcast.WindowFuncExpr:
		return c.convertWindowFuncExpr(n)

	case *pcast.WindowSpec:
		return c.convertWindowSpec(n)

	case nil:
		return nil

	default:
		if debug.Active {
			log.Printf("dolphin.convert: Unknown node type %T\n", n)
		}
		return &ast.TODO{}
	}
}
