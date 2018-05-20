goflags := -v -i

local:
	go build $(goflags)
	killall -USR2 typed || ./typed&

n := typed.pw
host := xojoc.pw

upload:
	GOOS=linux GOARCH=amd64 go build $(goflags) -o $(n)_amd64
	#rsync -avz $(n)_amd64 root@xojoc.pw:/srv/www/poloo.pw/web/$(n)
	ssh -f -n root@$(host) 'cd /srv/www/typed.pw; mv -f $(n) $(n)_old'
	scp $(n)_amd64 root@$(host):/srv/www/typed.pw/$(n)
	rsync -avz --delete *.html *.css root@$(host):/srv/www/typed.pw/
	ssh -f -n root@$(host) 'cd /srv/www/typed.pw/ && (killall -USR2 $(n) || nohup ./$(n) > /dev/null 2> /dev/null < /dev/null &)'


lint:
	gometalinter --deadline 200s --disable golint --disable gotype --enable gosimple --enable staticcheck -j 1 ./...

