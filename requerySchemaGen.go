package main

import (
	"database/sql"
	_ "github.com/go-sql-driver/mysql"
	"github.com/chuckpreslar/inflect"
	"strings"
	"regexp"
	"strconv"
	"text/template"
	"os"
	"log"
	"gopkg.in/alecthomas/kingpin.v2"
)

type Property struct {
	Annotation string
	Field string
}

type DataClass struct {
	Version       string
	Package       string
	ClassBegin    string
	ClassEnd      string
	Model         string
	TableName     string
	DataClassName string
	Properties    []Property
}

func (dataClass *DataClass) AddItem(property Property) []Property {
	dataClass.Properties = append(dataClass.Properties, property)
	return dataClass.Properties
}

var version = "0.0.5"

var (
	kotlinPackage = kingpin.Flag("package", "Kotlin package").Default("model").Short('p').String()
	dataSourceName = kingpin.Flag("datasource", "Database conncetion string (user:pass@/database)").Required().Short('d').String()
	generateInterface = kingpin.Flag("interface", "Generate Kotlin interfaces, otherwise it will generate Kotlin data classes").Short('i').Bool()
	path = kingpin.Flag("path", "Output path").Default("./gen").String()
)


const TEMPLATE string = `package {{.Package}}

// Generated by requerySchemaGen v{{.Version}}

import io.requery.*

@Table(name = "{{.TableName}}")
@Entity(model = "{{.Model}}")
{{.ClassBegin}}
{{range .Properties}}
    {{.Annotation}}
    {{.Field}}
{{end}}
{{.ClassEnd}}
`


func main() {
	kingpin.Version(version)
	kingpin.Parse()

	db, err := sql.Open("mysql", *dataSourceName)
	if err != nil {
		panic(err.Error())
	}
	defer db.Close()

	rows, err := db.Query("SHOW TABLES")
	checkErr(err)
	for rows.Next() {
		var tableName string
		err := rows.Scan(&tableName)
		checkErr(err)

		dataClass := parseTableDefinition(tableName, db)

		os.Mkdir(*path, 0700)
		fileName := strings.TrimRight(*path, "/") + "/" + dataClass.DataClassName + ".kt"
		os.Remove(fileName)

		f, err := os.Create(fileName)
		checkErr(err)

		t := template.New("data class")
		t, _ = t.Parse(TEMPLATE)
		t.Execute(f, dataClass)

		f.Close()
	}
}

func parseTableDefinition(tableName string, db *sql.DB) DataClass {
	dataClassName := inflect.Singularize(tableName)
	dataClassName = inflect.UpperCamelCase(dataClassName)

	classBegin := "data class " + dataClassName + " constructor("
	classEnd := ")"
	model := "ktdata"
	if *generateInterface {
		classBegin = "interface " + dataClassName + " {"
		classEnd = "}"
		model = "kt"
	}
	dataClass := DataClass{
		Version: version,
		Package: *kotlinPackage,
		ClassBegin: classBegin,
		ClassEnd: classEnd,
		Model: model,
		TableName: tableName,
		DataClassName: dataClassName,
	}

	rows, err := db.Query("DESCRIBE " + tableName)
	checkErr(err)

	for rows.Next() {
		var clnName, clnType, clnNull, clnKey, clnDefault, clnExtra sql.NullString
		err := rows.Scan(&clnName, &clnType, &clnNull, &clnKey, &clnDefault, &clnExtra)
		checkErr(err)

		nullableType := ""

		annotation := ""
		if clnKey.String == "PRI" {
			annotation += " @get:Key "
			//nullableType = "?"
		}
		if clnExtra.String == "auto_increment" {
			annotation += " @get:Generated "
		}

		columnAnnotation := "@get:Column(name=\"" + clnName.String + "\", "

		dataType, length, nullable := convertType(
			clnName.String,
			clnType.String,
			clnNull.String,
			clnDefault.String,
			tableName + "." + clnName.String,
		)
		if length != -1 {
			columnAnnotation += "length=" + strconv.Itoa(length) + ", "
		}
		// TODO value, unique, index
		if nullable {
			nullableType = "?"
			columnAnnotation += "nullable=true, "
		} else {
			columnAnnotation += "nullable=false, "
		}

		// remove is_ is dataType is boolean
		// TODO Make sure clnType is of an int family
		if dataType == "Boolean" && strings.HasPrefix(clnName.String, "is_") {
			clnName.String = strings.Replace(clnName.String, "is_", "", 1)
		}

		annotation += " " + strings.TrimRight(columnAnnotation, ", ") + ") "

		field := "var " + inflect.LowerCamelCase(clnName.String) + ": " + dataType + nullableType + ","
		if *generateInterface {
			field = strings.TrimRight(field, ",")
		}

		dataClass.AddItem(Property{
			Annotation: strings.TrimSpace(annotation),
			Field: field,
		})
	}

	// Remove command from the last property
	size := len(dataClass.Properties)
	dataClass.Properties[size-1].Field = strings.TrimRight(dataClass.Properties[size-1].Field, ",")

	return dataClass
}

func convertType(dbClnName string, dbType string, dbNullable, dbDefault, column string) (dataType string, length int, nullable bool) {
	// https://dev.mysql.com/doc/connector-j/5.1/en/connector-j-reference-type-conversions.html
	length = -1
	nullable = false
	if dbNullable == "YES" {
		nullable = true
	}

	// special boolean case
	if strings.HasPrefix(dbClnName, "is_") {
		dataType = "Boolean"
		return dataType, length, nullable
	}

	switch {
	case strings.HasPrefix(dbType, "int"), strings.HasPrefix(dbType, "tinyint"), strings.HasPrefix(dbType, "smallint"):
		dataType = "Int"

	// bigint
	case strings.HasPrefix(dbType, "bigint"):
		dataType = "Long"
		log.Printf("%s - %s", column, dbType)

	// decimal
	case strings.HasPrefix(dbType, "decimal"):
		dataType = "java.math.BigDecimal"
		log.Printf("%s - %s", column, dbType)

	// float
	case strings.HasPrefix(dbType,"float"):
		dataType = "Float"

	// double
	case strings.HasPrefix(dbType,"double"):
		dataType = "Double"
		log.Printf("%s - %s", column, dbType)

	// varchar, char
	case strings.HasPrefix(dbType, "varchar"), strings.HasPrefix(dbType, "char"):
		dataType = "String"
		length = getDbTypeLength(dbType)

	// enum
	case strings.HasPrefix(dbType, "enum"):
		dataType = "String"
		log.Printf("%s - %s", column, dbType)

	// timestamp
	case dbType == "timestamp":
		dataType = "java.time.ZonedDateTime"
		// Override nullable
		if dbDefault == "0000-00-00 00:00:00" {
			nullable = true
		}

	// date, datetime
	case dbType == "date", dbType == "datetime":
		dataType = "java.time.ZonedDateTime"
		log.Printf("%s - %s", column, dbType)

	// tinytext
	case dbType == "tinytext":
		dataType = "String"
		length = 255

	// text
	case dbType == "text":
		dataType = "String"
		length = 65535

	// mediumtext
	case dbType == "mediumtext":
		dataType = "String"
		length = 16777215

	// longtext
	case dbType == "longtext":
		dataType = "String"
		// TODO too big for int
		//length = 4294967295

	// longblob, binary
	case dbType == "longblob", strings.HasPrefix(dbType, "binary"):
		dataType = "ByteArray"
		log.Printf("%s - %s", column, dbType)

	default:
		panic("Type not supported: " + dbType + " - " + column)
	}

	return dataType, length, nullable
}

func getDbTypeLength(dbType string) int {
	r := regexp.MustCompile(`\((\d+)\)`)

	result := r.FindStringSubmatch(dbType)
	if len(result) == 2 {
		length, err := strconv.Atoi(result[1])
		if err != nil {
			return -1
		}

		return length
	}

	return -1
}

func checkErr(err error) {
	if err != nil {
		panic(err)
	}
}
