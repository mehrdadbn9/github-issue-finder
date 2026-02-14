#!/bin/bash

NOTIFICATION_DIR="/tmp/github-issue-finder-logs"
NOTIFICATION_FILE="$NOTIFICATION_DIR/notifications.log"

mkdir -p "$NOTIFICATION_DIR"
touch "$NOTIFICATION_FILE"

echo "📢 Notification Bridge Started"
echo "Watching: $NOTIFICATION_FILE"
echo "Press Ctrl+C to stop"
echo "-----------------------------------"

# Check if inotifywait is available
if command -v inotifywait &> /dev/null; then
    # Use inotify for real-time monitoring (more efficient)
    echo "✅ Using inotify mode"
    
    while true; do
        # Wait for the file to be modified
        inotifywait -q -e modify "$NOTIFICATION_FILE" &>/dev/null
        
        # Read and process new lines
        while IFS= read -r line; do
            if [[ -n "$line" && ! "$line" =~ ^[[:space:]]*$ ]]; then
                # Parse the line (format: "TITLE|URL|SCORE|PRIORITY")
                IFS='|' read -r TITLE URL SCORE PRIORITY <<< "$line"
                
                if [[ -n "$TITLE" ]]; then
                    # Determine urgency and icon based on priority
                    case "$PRIORITY" in
                        "High")
                            URGENCY="critical"
                            ICON="software-update-urgent"
                            ;;
                        "Medium")
                            URGENCY="normal"
                            ICON="software-update"
                            ;;
                        "Low"|"")
                            URGENCY="low"
                            ICON="dialog-information"
                            ;;
                        *)
                            URGENCY="normal"
                            ICON="dialog-information"
                            ;;
                    esac
                    
                    # Send notification
                    notify-send \
                        --urgency="$URGENCY" \
                        --icon="$ICON" \
                        --category="network" \
                        "🔍 New GitHub Issue Found!" \
                        "<b>$TITLE</b>\nScore: $SCORE | Priority: $PRIORITY\n\nClick to view"
                    
                    echo "🔔 Sent notification: $TITLE (Score: $SCORE)"
                fi
            fi
        done < <(tail -n +1 "$NOTIFICATION_FILE" | tail -n 20)
    done
else
    # Fallback to polling (less efficient but works)
    echo "⚠️  inotifywait not found, installing..."
    sudo apt-get install -y inotify-tools
    
    if [ $? -eq 0 ]; then
        echo "✅ Installed inotify-tools, restarting..."
        exec "$0"
    else
        echo "❌ Could not install inotify-tools, using polling mode"
        
        last_line=0
        while true; do
            if [[ -f "$NOTIFICATION_FILE" ]]; then
                current_lines=$(wc -l < "$NOTIFICATION_FILE")
                
                if [[ $current_lines -gt $last_line ]]; then
                    # Read new lines
                    sed -n "$((last_line + 1)),$current_lines p" "$NOTIFICATION_FILE" | while IFS= read -r line; do
                        if [[ -n "$line" && ! "$line" =~ ^[[:space:]]*$ ]]; then
                            IFS='|' read -r TITLE URL SCORE PRIORITY <<< "$line"
                            
                            if [[ -n "$TITLE" ]]; then
                                notify-send \
                                    --urgency="normal" \
                                    --icon="dialog-information" \
                                    "🔍 New GitHub Issue Found!" \
                                    "<b>$TITLE</b>\nScore: $SCORE\n\nClick to view"
                                
                                echo "🔔 Sent notification: $TITLE (Score: $SCORE)"
                            fi
                        fi
                    done
                    last_line=$current_lines
                fi
            fi
            
            sleep 1
        done
    fi
fi
