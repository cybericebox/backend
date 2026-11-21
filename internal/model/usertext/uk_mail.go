package usertext

// Texts of the mail dispatch states, the mail settings test and the mail footers.

const (
	// DeferredPrefix starts every deferral text of a mail target; stored rows
	// hold it, so ClassifyMailError recognises deferrals by it.
	DeferredPrefix = "Відкладено"
	// DeferredQuota is the target message when the daily mail limit is reached.
	DeferredQuota = DeferredPrefix + ": вичерпано добовий ліміт"
	// DeferredRate is the target message when the send rate is exceeded.
	DeferredRate = DeferredPrefix + ": перевищено швидкість відправки"
	// SkippedReserved is the target message for a reserved (RFC 2606) domain.
	SkippedReserved = "Пропущено: зарезервований домен (RFC 2606), лист не надсилався"
)

const (
	// TestMailSubject is the subject of the SMTP settings test mail.
	TestMailSubject = "Перевірка пошти Cyber ICE Box"
	// TestMailLabelPlatform and TestMailLabelEvent name the settings under test.
	TestMailLabelPlatform = "платформи"
	TestMailLabelEvent    = "заходу"
)

// TestMailText is the plain body of the SMTP settings test mail.
func TestMailText(label string) string {
	return "Це тестовий лист: налаштування SMTP " + label + " працюють."
}

// TestMailHTML is the HTML body of the SMTP settings test mail.
func TestMailHTML(label string) string {
	return "<p>" + TestMailText(label) + "</p>"
}

// DefaultFooterText is the built-in Ukrainian footer in the legacy text form.
const DefaultFooterText = "Цей лист надіслано автоматично. [Звʼязатися з нами]({reply_to})\n{platform_name}"

// FooterLinkReplacements are old/new pairs that keep the familiar Ukrainian
// footer labels together (the apostrophe in three spellings).
var FooterLinkReplacements = []string{
	"Звʼязатися з нами", "Звʼязатися&nbsp;з&nbsp;нами",
	"Зв\u2019язатися з нами", "Зв\u2019язатися&nbsp;з&nbsp;нами",
	"Зв&#39;язатися з нами", "Зв&#39;язатися&nbsp;з&nbsp;нами",
	"Політика конфіденційності", "Політика&nbsp;конфіденційності",
}

// Labels of the error journal mail.
const (
	ErrorMailSource      = "Джерело"
	ErrorMailTotal       = "Випадків"
	ErrorMailRequest     = "Запит"
	ErrorMailUser        = "Користувач"
	ErrorMailOpenError   = "Відкрити помилку"
	ErrorMailOpenJournal = "Відкрити журнал"
	ErrorMailDetails     = "Деталі помилки"
)
