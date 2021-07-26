package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/vjeantet/goldap/message"
	ldap "github.com/vjeantet/ldapserver"
)

var csv_records []
csv_records = make([][]string , 1)]

func main() {
	sqlserver, sqluser, sqlpass, sqldb := getCreds()
	log.Printf("DB Connection: Server=%s User=%s DB=%s", sqlserver, sqluser, sqldb)

	err := SQLConnect(sqlserver, sqluser, sqlpass, sqldb)
	if err != nil {
		log.Printf("DB ERROR: %s", err.Error())
		return
	}

	csvpath := getEnvVar("CSV_PATH", "")
	if csvpath != "" {
		csv_records = readCsvFile(csvpath)
	}

	//Create a new LDAP Server
	server := ldap.NewServer()

	//Create routes bindings
	routes := ldap.NewRouteMux()

	routes.Bind(handleBind)
	routes.Search(handleSearchDSE).Label("Search - Generic")

	//Attach routes to server
	server.Handle(routes)

	// listen on 10389 and serve
	go server.ListenAndServe(":10389")

	// When CTRL+C, SIGINT and SIGTERM signal occurs
	// Then stop server gracefully
	ch := make(chan os.Signal)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	close(ch)

	server.Stop()
}

func getEnvVar(key, fallback string) string {
    value, exists := os.LookupEnv(key)
    if !exists {
        value = fallback
    }
    return value
}

func getCreds() (string, string, string, string){
	sqlserver := getEnvVar("FREEPBX_SQLSERVER", "127.0.0.1:3306")
	sqluser := getEnvVar("FREEPBX_SQLUSER", "root")
	sqlpass := getEnvVar("FREEPBX_SQLPASS", "")
	sqldb := getEnvVar("FREEPBX_SQLDB", "asterisk")
	return sqlserver, sqluser, sqlpass, sqldb
}

func handleBind(w ldap.ResponseWriter, m *ldap.Message) {
	res := ldap.NewBindResponse(ldap.LDAPResultSuccess)
	w.Write(res)
}

func handleSearchDSE(w ldap.ResponseWriter, m *ldap.Message) {
	r := m.GetSearchRequest()

	res := ldap.NewSearchResultDoneResponse(ldap.LDAPResultSuccess)
	defer w.Write(res)

	log.Printf("Request BaseDn=%s", r.BaseObject())
	log.Printf("Request Filter=%#v", r.Filter())
	log.Printf("Request FilterString=%s", r.FilterString())
	log.Printf("Request Attributes=%s", r.Attributes())
	log.Printf("Request TimeLimit=%d", r.TimeLimit().Int())
	log.Printf("Request SizeLimit=%d", r.SizeLimit().Int())

	sql := "SELECT name, extension FROM users"
	sqlVals := []interface{}{}

	swapField := func(v string) string {
		switch v {
		case "displayName":
			return "name"
		case "telephoneNumber":
			return "extension"
		default:
			log.Printf("Invalid Field Name (%s), returned name", v)
			return "name"
		}
	}

	getSubstringSearch := func(v []message.Substring) string {
		for _, fs := range v {
			switch fsv := fs.(type) {
			case message.SubstringInitial:
				return string(fsv) + "%"
			case message.SubstringAny:
				return "%" + string(fsv) + "%"
			case message.SubstringFinal:
				return "%" + string(fsv)
			}
		}
		return ""
	}

	var recursiveFilter func(filter interface{}, root bool) string
	recursiveFilter = func(filter interface{}, root bool) string {
		where := ""

		var filterProcessSub func(vsub interface{}) string
		filterProcessSub = func(vsub interface{}) string {
			switch vs := vsub.(type) {
			case message.FilterGreaterOrEqual:
				sqlVals = append(sqlVals, vs.AssertionValue())
				return swapField(string(vs.AttributeDesc())) + " >= ?"
			case message.FilterLessOrEqual:
				sqlVals = append(sqlVals, vs.AssertionValue())
				return swapField(string(vs.AttributeDesc())) + " <= ?"
			case message.FilterEqualityMatch:
				sqlVals = append(sqlVals, vs.AssertionValue())
				return swapField(string(vs.AttributeDesc())) + " = ?"
			case message.FilterSubstrings:
				sqlVals = append(sqlVals, getSubstringSearch(vs.Substrings()))
				return swapField(string(vs.Type_())) + " LIKE ?"
			case message.FilterAnd:
				return recursiveFilter(vs, false)
			case message.FilterOr:
				return recursiveFilter(vs, false)
			case message.FilterNot:
				return " NOT ( " + filterProcessSub(vs.Filter) + " ) "
			default:
				return ""
			}
			return ""
		}

		switch val := filter.(type) {
		case message.FilterAnd:
			i := 0
			for _, vsub := range val {
				addWhere := func() {
					if i > 0 {
						where += " AND "
					}
					i++
				}
				if ret := filterProcessSub(vsub); ret != "" {
					addWhere()
					where += ret
				}
			}
		case message.FilterOr:
			i := 0
			for _, vsub := range val {
				addWhere := func() {
					if i > 0 {
						where += " OR "
					}
					i++
				}
				if ret := filterProcessSub(vsub); ret != "" {
					addWhere()
					where += ret
				}
			}
		case message.FilterSubstrings:
			if ret := filterProcessSub(val); ret != "" {
				where += ret
			}
		default:
			log.Printf("Searching without filter...")
		}

		if where != "" {
			if root {
				where = " WHERE " + where
			} else {
				where = " ( " + where + " ) "
			}
		}

		return where
	}

	sql += " " + recursiveFilter(r.Filter(), true) + " "

	sql += " ORDER BY name ASC LIMIT 0, ?"
	if r.SizeLimit().Int() > 0 {
		sqlVals = append(sqlVals, r.SizeLimit().Int())
	} else {
		sqlVals = append(sqlVals, 99)
	}

	log.Printf("Query SQL: %s %#v", sql, sqlVals)
	result, err := SQLSearch(sql, sqlVals)
	if err != nil {
		log.Printf("SQL ERROR: %s", err)
	}

	e := ldap.NewSearchResultEntry("")
	for _, entry := range result {
		e.AddAttribute("displayName", message.AttributeValue(entry.Name))
		e.AddAttribute("telephoneNumber", message.AttributeValue(entry.Extension))
	}
	w.Write(e)
}
