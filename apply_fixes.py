[
    1.0,
    'Add godotenv import\n    \'sed -i "19 a\\    \\"github.com/joho/godotenv\\"\\n    _ \\"github.com/lib/pq\\"',
    "main.go',\n    \n    # 2. Make Telegram optional  \n    'sed -i",
    's/return nil, fmt.Errorf(\\"failed to create Telegram bot: %w\\',
    'err)/log.Printf(\\"Warning: Failed to connect to Telegram API: %v\\',
    "err)\\\\n\\\\t\\\\tbot = nil/",
    "main.go',\n    \n    # 3. Limit projects to 5\n    'sed -i",
    "s/for _, project := range f.projects/for i, project := range f.projects {\\\\n\\\\t\\\\tif i >= 5 {\\\\n\\\\t\\\\t\\\\tbreak\\\\n\\\\t\\\\t}/",
    "main.go',\n]\n\nfor cmd in commands:\n    print(f\"Running: {cmd}",
    'try:\n        subprocess.run(cmd, shell=True, check=True)\n    except subprocess.CalledProcessError as e:\n        print(f"Error: {e}")\n        sys.exit(1)\n\nprint("Changes applied successfully!',
]
