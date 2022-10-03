package dbase

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Containing DBF header information like dBase FileType, last change and rows count.
// https://docs.microsoft.com/en-us/previous-versions/visualstudio/foxpro/st4a0s68(v=vs.80)#table-header-record-structure
type Header struct {
	FileType   byte     // File type flag
	Year       uint8    // Last update year (0-99)
	Month      uint8    // Last update month
	Day        uint8    // Last update day
	RowsCount  uint32   // Number of rows in file
	FirstRow   uint16   // Position of first data row
	RowLength  uint16   // Length of one data row, including delete flag
	Reserved   [16]byte // Reserved
	TableFlags byte     // Table flags
	CodePage   byte     // Code page mark
}

// Column is a struct containing the column information
type Column struct {
	ColumnName [11]byte // Column name with a maximum of 10 characters. If less than 10, it is padded with null characters (0x00).
	DataType   byte     // Column type
	Position   uint32   // Displacement of column in row
	Length     uint8    // Length of column (in bytes)
	Decimals   uint8    // Number of decimal places
	Flags      byte     // Column flags
	Next       uint32   // Value of autoincrement Next value
	Step       uint16   // Value of autoincrement Step value
	Reserved   [8]byte  // Reserved
}

// Table is a struct containing the table columns, modifications and the row pointer
type Table struct {
	// Columns defined in this table
	columns []*Column
	// Modification to change values or name of fields
	mods []*Modification
	// Internal row pointer, can be moved
	rowPointer uint32
	// Trimspaces default value
	trimSpaces bool
}

// Row is a struct containing the row Position, deleted flag and data fields
type Row struct {
	dbf      *DBF // Pointer to the DBF object this row belongs to
	Position uint32
	Deleted  bool
	fields   []*Field
}

// Field is a row data field
type Field struct {
	column *Column
	value  interface{}
}

// Modification allows to change the column name or value type
type Modification struct {
	TrimSpaces  bool
	Convert     func(interface{}) (interface{}, error)
	ExternalKey string
}

/**
 *	################################################################
 *	#					dBase header helpers
 *	################################################################
 */

// Parses the year, month and day to time.Time.
// Note: the year is stored in 2 digits, so we assume the year is between 2000 and 2099.
func (h *Header) Modified() time.Time {
	return time.Date(2000+int(h.Year), time.Month(h.Month), int(h.Day), 0, 0, 0, 0, time.Local)
}

// Returns the calculated number of columns from the header info alone (without the need to read the columninfo from the header).
// This is the fastest way to determine the number of rows in the file.
// Note: when Open is used the columns have already been parsed so it is better to call DBF.ColumnsCount() in that case.
func (h *Header) ColumnsCount() uint16 {
	return (h.FirstRow - 296) / 32
}

// Returns the calculated file size based on the header info
func (h *Header) FileSize() int64 {
	return 296 + int64(h.ColumnsCount()*32) + int64(h.RowsCount*uint32(h.RowLength))
}

/**
 *	################################################################
 *	#						DBF helper
 *	################################################################
 */

// Returns if the internal row pointer is at end of file
func (dbf *DBF) EOF() bool {
	return dbf.table.rowPointer >= dbf.header.RowsCount
}

// Returns if the internal row pointer is before first row
func (dbf *DBF) BOF() bool {
	return dbf.table.rowPointer == 0
}

// Returns the current row pointer position
func (dbf *DBF) Pointer() uint32 {
	return dbf.table.rowPointer
}

// Returns the dBase database file header struct for inspecting
func (dbf *DBF) Header() *Header {
	return dbf.header
}

// returns the number of rows
func (dbf *DBF) RowsCount() uint32 {
	return dbf.header.RowsCount
}

// Returns all columns
func (dbf *DBF) Columns() []*Column {
	return dbf.table.columns
}

// Returns the number of columns
func (dbf *DBF) ColumnsCount() uint16 {
	return uint16(len(dbf.table.columns))
}

// Returns a slice of all the column names
func (dbf *DBF) ColumnNames() []string {
	num := len(dbf.table.columns)
	names := make([]string, num)
	for i := 0; i < num; i++ {
		names[i] = dbf.table.columns[i].Name()
	}
	return names
}

// Returns the column position of a column by name or -1 if not found.
func (dbf *DBF) ColumnPosByName(colname string) int {
	for i := 0; i < len(dbf.table.columns); i++ {
		if dbf.table.columns[i].Name() == colname {
			return i
		}
	}
	return -1
}

// Returns the column position of a column or -1 if not found.
func (dbf *DBF) ColumnPos(column *Column) int {
	for i := 0; i < len(dbf.table.columns); i++ {
		if dbf.table.columns[i] == column {
			return i
		}
	}
	return -1
}

/**
 *	################################################################
 *	#						Modifications
 *	################################################################
 */

// SetColumnModification sets a modification for a column
func (dbf *DBF) SetColumnModification(position int, trimspaces bool, key string, convert func(interface{}) (interface{}, error)) {
	// Skip if position is out of range
	if position < 0 || position >= len(dbf.table.columns) {
		return
	}
	dbf.table.mods[position] = &Modification{
		TrimSpaces:  trimspaces,
		Convert:     convert,
		ExternalKey: key,
	}
}

// Set the default trimspaces value for all columns
func (dbf *DBF) SetTrimspacesDefault(b bool) {
	dbf.table.trimSpaces = b
}

// Returns the column modification for a column at the given position
func (dbf *DBF) GetColumnModification(position int) *Modification {
	return dbf.table.mods[position]
}

/**
 *	################################################################
 *	#						ColumnHeader helper
 *	################################################################
 */

// Returns the name of the column as a trimmed string (max length 10)
func (c *Column) Name() string {
	return string(bytes.TrimRight(c.ColumnName[:], "\x00"))
}

// Returns the type of the column as string (length 1)
func (c *Column) Type() string {
	return string(c.DataType)
}

/**
 *	################################################################
 *	#						Rows
 *	################################################################
 */

// Returns all rows as a slice
func (dbf *DBF) Rows(skipInvalid bool, skipDeleted bool) ([]*Row, error) {
	rows := make([]*Row, 0)
	for !dbf.EOF() {
		// This reads the complete row
		row, err := dbf.Row()
		if err != nil && !skipInvalid {
			return nil, fmt.Errorf("dbase-table-rows-1:FAILED:%w", err)
		}
		// Increment the row pointer
		dbf.Skip(1)
		// skip deleted rows
		if row.Deleted && skipDeleted {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// Returns the requested row at dbf.rowPointer.
func (dbf *DBF) Row() (*Row, error) {
	data, err := dbf.readRow(dbf.table.rowPointer)
	if err != nil {
		return nil, fmt.Errorf("dbase-table-get-row-1:FAILED:%w", err)
	}
	return dbf.BytesToRow(data)
}

// Converts raw row data to a Row struct
// If the data points to a memo (FPT) file this file is also read
func (dbf *DBF) BytesToRow(data []byte) (*Row, error) {
	rec := &Row{}
	rec.Position = dbf.table.rowPointer
	rec.dbf = dbf
	rec.fields = make([]*Field, dbf.ColumnsCount())
	if len(data) < int(dbf.header.RowLength) {
		return nil, fmt.Errorf("dbase-table-bytestorow-1:FAILED:invalid row data size %v Bytes < %v Bytes", len(data), int(dbf.header.RowLength))
	}
	// a row should start with te delete flag, a space ACTIVE(0x20) or DELETED(0x2A)
	rec.Deleted = data[0] == Deleted
	if !rec.Deleted && data[0] != Active {
		return nil, fmt.Errorf("dbase-table-bytestorow-2:FAILED:invalid row data, no delete flag found at beginning of row")
	}
	// deleted flag already read
	offset := uint16(1)
	for i := 0; i < len(rec.fields); i++ {
		column := dbf.table.columns[i]
		val, err := dbf.dataToValue(data[offset:offset+uint16(column.Length)], dbf.table.columns[i])
		if err != nil {
			return rec, fmt.Errorf("dbase-table-bytestorow-3:FAILED:%w", err)
		}
		rec.fields[i] = &Field{
			column: column,
			value:  val,
		}
		offset += uint16(column.Length)
	}
	return rec, nil
}

// Returns a new Row struct with the same column structure as the dbf and the next row pointer
func (dbf *DBF) NewRow() *Row {
	return &Row{
		dbf:      dbf,
		Position: dbf.header.RowsCount + 1,
		Deleted:  false,
		fields:   make([]*Field, len(dbf.table.columns)),
	}
}

// Writes the row to the file at the row pointer position
func (row *Row) Write() error {
	return row.writeRow()
}

// Increments the pointer s row to the end of the file
func (row *Row) Add() error {
	row.Position = row.dbf.header.RowsCount + 1
	return row.Write()
}

// Returns all fields of the current row
func (row *Row) Fields() []*Field {
	return row.fields
}

// Returns the field of a row by position
func (row *Row) Field(pos int) (*Field, error) {
	if pos < 0 || len(row.fields) < pos {
		return nil, fmt.Errorf("dbase-table-field-1:FAILED:%v", InvalidPosition)
	}
	return row.fields[pos], nil
}

// Returns all values of a row as a slice of interface{}
func (row *Row) Values() []interface{} {
	values := make([]interface{}, 0)
	for _, field := range row.fields {
		values = append(values, field.value)
	}
	return values
}

// ChangeField applies a modificated field to the row
func (row *Row) ChangeField(field *Field) error {
	if field.column == nil {
		return fmt.Errorf("dbase-table-changefield-1:FAILED:Column missing")
	}
	pos := row.dbf.ColumnPos(field.column)
	if pos < 0 || len(row.fields) < pos {
		return fmt.Errorf("dbase-table-changefield-2:FAILED:%v", InvalidPosition)
	}
	row.fields[pos] = field
	return nil
}

// SetValue allows to change the field value
func (field *Field) SetValue(value interface{}) {
	field.value = value
}

// Value returns the field value
func (field *Field) GetValue() interface{} {
	return field.value
}

// Name returns the field name
func (field *Field) Name() string {
	return field.column.Name()
}

// Type returns the field type
func (field *Field) Type() string {
	return field.column.Type()
}

// Column returns the field column definition
func (field *Field) Column() *Column {
	return field.column
}

/**
 *	################################################################
 *	#						Conversions
 *	################################################################
 */

// Converts the row back to raw dbase data
func (row *Row) ToBytes() ([]byte, error) {
	data := make([]byte, row.dbf.header.RowLength)
	// a row should start with te delete flag, a space ACTIVE(0x20) or DELETED(0x2A)
	if row.Deleted {
		data[0] = Deleted
	} else {
		data[0] = Active
	}
	// deleted flag already read
	offset := uint16(1)
	for _, field := range row.fields {
		val, err := row.dbf.valueToData(field)
		if err != nil {
			return nil, fmt.Errorf("dbase-table-rowtobytes-1:FAILED:%w", err)
		}
		copy(data[offset:offset+uint16(field.column.Length)], val)
		offset += uint16(field.column.Length)
	}

	return data, nil
}

// Returns a complete row as a map.
func (row *Row) ToMap() (map[string]interface{}, error) {
	out := make(map[string]interface{})
	var err error
	for i, field := range row.fields {
		val := field.GetValue()
		mod := row.dbf.table.mods[i]
		if mod != nil {
			if row.dbf.table.trimSpaces && mod.TrimSpaces || mod.TrimSpaces {
				if str, ok := val.(string); ok {
					val = strings.TrimSpace(str)
				}
			}
			if mod.Convert != nil {
				val, err = mod.Convert(val)
				if err != nil {
					return nil, fmt.Errorf("dbase-table-to-map-1:FAILED:%w", err)
				}
			}
			if len(mod.ExternalKey) != 0 {
				out[mod.ExternalKey] = val
				continue
			}
		}
		out[field.Name()] = val
	}
	return out, nil
}

// Returns a complete row as a JSON object.
func (row *Row) ToJSON() ([]byte, error) {
	m, err := row.ToMap()
	if err != nil {
		return nil, fmt.Errorf("dbase-table-row-to-json-1:FAILED:%w", err)
	}
	j, err := json.Marshal(m)
	if err != nil {
		return j, fmt.Errorf("dbase-table-row-to-json-2:FAILED:%w", err)
	}
	return j, nil
}

// Parses the row from map to JSON-encoded and from there to a struct and stores the result in the value pointed to by v.
// Just a convenience function to avoid the intermediate JSON step.
func (row *Row) ToStruct(v interface{}) error {
	jsonRow, err := row.ToJSON()
	if err != nil {
		return fmt.Errorf("dbase-table-to-struct-1:FAILED:%w", err)
	}
	err = json.Unmarshal(jsonRow, v)
	if err != nil {
		return fmt.Errorf("dbase-table-to-struct-2:FAILED:%w", err)
	}
	return nil
}

// Converts a map of interfaces into the row representation
func (dbf *DBF) RowFromMap(m map[string]interface{}) (*Row, error) {
	row := dbf.NewRow()
	for i := range row.fields {
		field := &Field{column: dbf.table.columns[i]}
		mod := dbf.table.mods[i]
		if mod != nil {
			if len(mod.ExternalKey) != 0 {
				if val, ok := m[mod.ExternalKey]; ok {
					field.value = val
				}
				continue
			}
		}
		if val, ok := m[field.Name()]; ok {
			field.value = val
		}
		row.fields[i] = field
	}
	return row, nil
}

// Converts a JSON-encoded row into the row representation
func (dbf *DBF) RowFromJSON(j []byte) (*Row, error) {
	m := make(map[string]interface{})
	err := json.Unmarshal(j, &m)
	if err != nil {
		return nil, fmt.Errorf("dbase-table-from-json-1:FAILED:%w", err)
	}
	row, err := dbf.RowFromMap(m)
	if err != nil {
		return nil, fmt.Errorf("dbase-table-from-json-2:FAILED:%w", err)
	}
	return row, nil
}

// Converts a struct into the row representation
func (dbf *DBF) RowFromStruct(v interface{}) (*Row, error) {
	j, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("dbase-table-from-struct-1:FAILED:%w", err)
	}
	row, err := dbf.RowFromJSON(j)
	if err != nil {
		return nil, fmt.Errorf("dbase-table-from-struct-2:FAILED:%w", err)
	}
	return row, nil
}
