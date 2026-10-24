package views

type ConnectionRow struct {
	ID, Name, DBMS, Address, Username, TLS string
	Active                                 bool
}

type ConnectionForm struct {
	ID, Name, DBMS, Host, Port, Database, Username, TLS string
	Active, Existing, Sandbox                           bool
}

type ConnectionCapability struct {
	Name      string
	Supported bool
}

type ConnectionTest struct {
	Ran, OK bool
	Message string
}

type ConnectionPageData struct {
	CSRFToken, Username, Message, Error string
	Connections                         []ConnectionRow
	Form                                ConnectionForm
	Capabilities                        []ConnectionCapability
	ReceiptSQL                          string
	Test                                ConnectionTest
}

func connectionURL(id string) string { return "/admin/databases/" + id }
