package views

type PlanNodeView struct {
	Type, Detail                  string
	Cost, Rows                    string
	ActualTime, ActualRows, Loops string
	Children                      []PlanNodeView
}

type PlanView struct {
	Dialect, Mode, SQLHash      string
	Warnings                    []string
	PlanningTime, ExecutionTime string
	Root                        PlanNodeView
}

func explainURL(databaseID string) string { return databaseURL(databaseID) + "/explain" }
