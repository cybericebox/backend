package inappdefaults

// The reveal timeout alarm to the organizers of an event. Created at daemon start (see startupdefaults.Seed), like the
// resource calendar flow.
func init() {
	StartupSeeded["event.lab.reveal_timeout"] = true
	uk = append(uk,
		Template{"event.lab.reveal_timeout", "Завдання показано не всім командам", "Захід «{{.event_name}}», завдання «{{.task_name}}»: лабораторії не всіх команд були готові через {{.timeout_minutes}} хв, тож завдання показано командам із готовими лабораторіями. Ще чекають ({{.count}}): {{.teams}}. Кожна з них отримає завдання, щойно її лабораторія буде готова. Перевірте їх на сторінці «Лабораторії».", ""},
	)
	en = append(en,
		Template{"event.lab.reveal_timeout", "Tasks shown to some teams only", "Event «{{.event_name}}», task «{{.task_name}}»: not every team's lab was ready after {{.timeout_minutes}} min, so the task was shown to the teams with a ready lab. Still waiting ({{.count}}): {{.teams}}. Each of them gets the task as soon as its lab is ready. Check them on the «Labs» page.", ""},
	)
}
