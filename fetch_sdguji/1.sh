for i in $(cat 1.txt); do

if [ "$(curl -s http://127.0.0.1:8080/v5/myapis/book/$i)" == "NOT_FOUND" ]; then

grep -w  "$i"  all_book.txt | head -1  

fi

done
