[
    "",
    [
        'j] and j > i:\n                email_alert = """\n\t\t\t\tif err := finder.SendEmailAlert(issues); err != nil {\n\t\t\t\t\tlog.Printf("Error sending email alert: %v", err)\n\t\t\t\t} else {\n\t\t\t\t\tlog.Printf("Successfully sent email alert for %d issues", len(issues))\n\t\t\t\t}\n"""\n                lines.insert(j + 1, email_alert)\n                print(f"Inserted email alert call after line {j}")\n                break\n        break\n\n# Write back\nwith open(\'main.go\', \'w\') as f:\n    f.writelines(lines)'
    ],
]
