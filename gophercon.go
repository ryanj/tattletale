package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"runtime"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
	"unicode"
)

const (
	ListenAddr   = "0.0.0.0:4001"
	TurnTimeout  = 40 * time.Second
	TotalTimeout = 120 * time.Second
	JoinTimeout  = 30 * time.Second
	WaitTimeout  = 20 * time.Second
	NumPlayers   = 3
)

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	rand.Seed(time.Now().UnixNano())
	l, err := net.Listen("tcp", ListenAddr)
	if err != nil {
		log.Fatal(err)
	}
	for {
		c, err := l.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go lobby(c)
	}
}

func lobby(c io.ReadWriteCloser) {
	//printMenu(c)

	/*
		var cmd int
		_, err := fmt.Fscanln(c, &cmd)
		if err != nil {
			c.Close()
			return
		}
	*/

	/*
		switch cmd {
		case 1:
			match(c)
		case 2:
			setflag(c)
		case 3:
			getflag(c)
		default:
			c.Close()
			return
		}
	*/
	match(c)
}

func printMenu(c io.Writer) {
	fmt.Fprintln(c, "Enter the number of a choice below:")
	fmt.Fprintln(c, "    1) Show up for work ")
	fmt.Fprintln(c, "    2) Create target room")
	fmt.Fprintln(c, "    3) Get target room value")
}

// Thread safe map for storing clients entering rooms. Each room_id has its own
// channel to queue up new connections.
var cStore = NewCStore()

// Thread safe map implementation for connection management
// for the flag_id, password, flag DB. Design from talk by Andrew
// Gerrand: http://www.youtube.com/watch?feature=player_embedded&v=2-pPAvqyluI
type CStore struct {
	rooms map[string]chan io.ReadWriteCloser
	mu    sync.RWMutex
}

func (s *CStore) Get(key string) chan io.ReadWriteCloser {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rooms[key]
}
func (s *CStore) Set(key string, room chan io.ReadWriteCloser) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, present := s.rooms[key]
	if present {
		return false
	}
	s.rooms[key] = room
	return true
}
func (s *CStore) Delete(key string) {
	s.mu.Lock()
	delete(s.rooms, key)
	s.mu.Unlock()
}
func NewCStore() *CStore {
	return &CStore{
		rooms: make(map[string]chan io.ReadWriteCloser),
	}
}

func getroomid(c io.ReadWriter) (string, error) {
	fmt.Fprintln(c, "A monolithic building appears before you. You have arrived at the office. Try not to act nervous.")
	fmt.Fprintln(c, "Log in to your team's assigned collaboration channel:")
	var cmd string
	_, err := fmt.Fscanln(c, &cmd)
	if err != nil {
		return cmd, fmt.Errorf("%v", "Invalid Room!")
	}
	return cmd, nil
}
func match(c io.ReadWriteCloser) {
	roomid, err := getroomid(c)
	if err != nil {
		fmt.Fprintln(c, "Invalid RoomID")
		c.Close()
		return
	} else if roomid == "641A" {
		fmt.Fprintln(c, "This room has not the data you are looking for. Get out!")
		c.Close()
		return
	}
	queue := make(chan io.ReadWriteCloser)
	// if queue doesn't exist create room
	if cStore.Set(roomid, queue) {
		log.Println("Launch Chat")
		go groupchat(queue, roomid)
	} else if queue = cStore.Get(roomid); queue == nil {
		fmt.Fprintln(c, "Room ID is in use!")
		c.Close()
		return
	}

	// enter queue
	select {
	case queue <- c:
	case <-time.After(WaitTimeout):
		fmt.Fprintln(c, "Room Full!")
		log.Println("waitlist timeout")
		c.Close()
	}
}

type File struct {
	Name  string
	Size  int
	Value int
	qty   int //used for knapsack, always 1
}

type Leaker struct {
	conn      io.ReadWriteCloser
	Files     map[string]File
	Bandwidth int
	isDone    bool
}

type GameState map[string]*Leaker

type Snowden struct {
	Init            bool     //starts false until he is messaged first time
	Endtoken        chan int //guards one person entering end room
	Files           map[string][]File
	SnowdenSolution int
	Roomid          string
}

func (s *Snowden) String() string {
	return "Glenda"
}

const prompt = "           > "

func groupchat(queue <-chan io.ReadWriteCloser, roomid string) {
	// collect connections
	conns := make([]io.ReadWriteCloser, 0, NumPlayers)
	for i := 0; i < NumPlayers; i++ {
		//conns[i] = <-queue
		select {
		case c := <-queue:
			conns = append(conns, c)
			broadcast(conns, textWrap("-->", fmt.Sprintf("Gopher%v has joined #%v, waiting for teammates...", i+1, roomid)))
		case <-time.After(JoinTimeout):
			broadcast(conns, textWrap("fail", "Not everyone showed up for work: Goodbye!"))
			cStore.Delete(roomid)
			for i := range conns {
				conns[i].Close()
			}
			log.Printf("Room never filled: %v\n", roomid)
			return
		}
	}
	// free roomid and use of queue
	cStore.Delete(roomid)
	broadcast(conns, textWrap("* --", "Everyone has arrived, mission starting..."))
	broadcast(conns, textWrap("* --", "Ask for /help to get familiar around here"))
	//broadcast(conns, prompt)

	// Initialize game state
	snowden := &Snowden{
		Init:     false,
		Endtoken: make(chan int, 1),
		Files:    make(map[string][]File, NumPlayers+1),
		Roomid:   roomid,
	}
	snowden.Endtoken <- 1
	gamestate := make(map[string]*Leaker, NumPlayers+2)
	for i := 0; i < NumPlayers; i++ {
		id := newID(i)
		gamestate[id] = &Leaker{
			conn:   conns[i],
			Files:  make(map[string]File),
			isDone: false,
		}
		snowden.Files[id] = []File{}
	}
	knapsackInit(gamestate)

	// set target solution
	snowden.SnowdenSolution = solveknapsack(gamestate)

	// start input worker per player
	errc := make(chan error, 1)
	inpc := make(chan *Message, 10)
	for id, v := range gamestate {
		go sendparser(v.conn, inpc, errc, id)
	}

	totalTime := time.After(TotalTimeout)
	go gamedispatch(gamestate, snowden, inpc, errc, totalTime)

	// wait for first error to close game
	if err := <-errc; err != nil {
		log.Println(err)
	}
	// close clients
	for i := range conns {
		conns[i].Close()
	}
	log.Printf("room shutdown: %v\n", roomid)

}

const (
	NumFiles = 15
)

var names = []string{"Gopher1", "Gopher2", "Gopher3", "Gopher4", "Gopher5"}

func newID(i int) string {
	return names[i]
}

var wordlist = []string{
	"RadicalPornEnthusiasts", "SIGINT", "BoundlessInformant",
	"TorStinks", "GCHQ", "EgoGiraffe", "PRISM", "641A", "XKEYSCORE",
	"PsyOps", "LOVEINT", "QuantumCookie", "FoxAcid", "G20", "BULLRUN",
	"RONIN", "Upstream", "SudanInvoice", "UnitedNationsInvestigation",
	"ThePasswordIsKittens", "PornFabricator", "TransparencyReport", "CitLabAttack",
	"RemoteControlSystem", "ODays", "ExploitDeliveryNetwork", "HackedTeam",
}
var extlist = []string{".ppt", ".doc"}

//InvoiceSudan
//UnitedNationsInvestigation
//thumbs.db
//ThePasswordIsKittens
//PornFabricator
//TransparencyReport
//CitLabAttacks
//NSAContract
//RemoteControlSystem
//ODayVault
//ExploitDeliveryNetwork

func newFileName(player, file int) string {
	index := NumFiles*player + file
	word := index / len(extlist)
	ext := index % len(extlist)
	return wordlist[word] + extlist[ext]

	/* random ppt files
	   b := make([]byte, 10)
	   rand.Read(b)
	   en := base32.StdEncoding
	   d := make([]byte, en.EncodedLen(len(b)))
	   en.Encode(d, b)
	   return string(d) + ".ppt"
	*/
}

const (
	sizerange   = 3000
	sizeoffset  = 200
	valuerange  = 90
	valueoffset = 10
	smallmod    = 5
	bigmod      = 5
)

func initFiles(fmap map[string]File, playernum int) (filesum int) {
	filesum = 0
	for i := 0; i < NumFiles; i++ {
		name := newFileName(playernum, i)
		size := (rand.Int() % sizerange) + sizeoffset
		filesum += size
		fmap[name] = File{
			Size:  size,
			Value: (rand.Int() % valuerange) + valueoffset,
			Name:  name,
			qty:   1,
		}
	}
	return filesum
}

func knapsackInit(s GameState) {
	i := 0
	var remaining = 0
	var luckyagent = rand.Int() % NumPlayers
	var luckyid string
	var luckysum int

	for id, v := range s {
		filesum := initFiles(v.Files, i)
		if i == luckyagent {
			luckyid = id
			luckysum = filesum
			i++
			continue
		} else {
			v.Bandwidth = (filesum * smallmod) / 10 //avoiding floats here with
			remaining += filesum - v.Bandwidth
		}
		i++
	}
	s[luckyid].Bandwidth = luckysum + (bigmod*remaining)/10

}

type Message struct {
	To   string
	From string
	Cmd  string
	Msg  string
}

//Commands!
const (
	LIST = "/list"
	WHO  = "/look"
	MSG  = "/msg"
	SEND = "/send"
	CAST = "/all"
	HELP = "/help"
	ERR  = "/err"
)

func sendparser(c io.ReadWriter, input chan<- *Message, errc chan<- error, id string) {
	scanner := bufio.NewScanner(c)

	for scanner.Scan() {
		var to, msg, cmd string
		s := strings.SplitN(scanner.Text(), " ", 3)
		switch strings.ToLower(s[0]) {
		case MSG:
			cmd = MSG
			if len(s) > 2 {
				to = s[1]
				msg = s[2]
			} else {
				fmt.Fprintln(c, textWrap("error", "'/msg' has 2 arguments"))
				//fmt.Fprint(c, prompt)
				continue
			}
		case SEND:
			cmd = SEND
			if len(s) > 2 {
				to = s[1]
				msg = s[2]
			} else {
				fmt.Fprintln(c, textWrap("error", "'/send' has 2 arguments"))
				//fmt.Fprint(c, prompt)
				continue
			}
		case LIST:
			cmd = LIST
			msg = ""
			to = id
		case WHO:
			cmd = WHO
			msg = ""
			to = id
		case HELP:
			cmd = HELP
			msg = ""
			to = id
		default:
			msg = scanner.Text()
			if strings.HasPrefix(msg, "/") {
				cmd = ERR
				to = id
			} else {
				to = "*"
				cmd = CAST
			}
		}
		message := &Message{
			To:   to,
			From: id,
			Msg:  msg,
			Cmd:  cmd,
		}
		input <- message //send to dispatch
	}
	if err := scanner.Err(); err != nil {
		select {
		case errc <- err:
		default:
			return
		}
	} else {
		select {
		case errc <- io.EOF: //apparently EOF is not an scanner err
		default:
			return
		}
	}
}

func broadcast(conns []io.ReadWriteCloser, msg string) {
	for i := range conns {
		fmt.Fprint(conns[i], msg)
	}
}

func broadcastmsg(g GameState, msg *Message) {
	for id, v := range g {
		if id != msg.From {
			fmt.Fprintf(v.conn, textWrap(msg.From, msg.Msg))
		}
		//fmt.Fprint(v.conn, prompt)
		/*
			else if id == msg.From {
				fmt.Fprintf(v.conn, textWrap(msg.From, msg.Msg))
			}
		*/
	}
}

func sendmsg(g GameState, msg *Message) {
	if _, ok := g[msg.To]; ok {
		fmt.Fprintf(g[msg.To].conn, textWrap("msg --", fmt.Sprintf("*msg from %v: %v", msg.From, msg.Msg)))
		sender := msg.From
		fmt.Fprintf(g[sender].conn, textWrap("msg --", fmt.Sprintf("*msg to %v: %v", msg.To, msg.Msg)))
	} else {
		fmt.Fprintf(g[msg.From].conn, textWrap("error", "Invalid Recipient ID"))
		//fmt.Fprintf(g[msg.From].conn, prompt)
	}

}

func listfiles(g GameState, msg *Message) {
	if v, ok := g[msg.From]; ok {
		if v.isDone {
			fmt.Fprintf(v.conn, textWrap("waiting", ""))
			return
		}
		w := new(tabwriter.Writer)
		w.Init(v.conn, 4, 0, 1, ' ', tabwriter.AlignRight)
		fmt.Fprintf(w, "   list -- | Remaining Bandwidth: %v KB\n", g[msg.From].Bandwidth)
		fmt.Fprintf(w, "  list -- |\tName\tSize\tSecrecy Value\t\n")

		order := make([]string, 0)
		for k := range v.Files {
			order = append(order, k)
		}
		sort.Strings(order)
		for _, k := range order {
			fmt.Fprintf(w, "  list -- |\t%v\t%vKB\t%v\t\n", k, v.Files[k].Size, v.Files[k].Value)
		}
		w.Flush()
		//fmt.Fprintf(v.conn, prompt)
	} else {
		log.Fatal("invalid sender ID")
	}
}
func help(g GameState, msg *Message) {
	p := "   help -- | "
	if v, ok := g[msg.From]; ok {
		fmt.Fprintln(v.conn, p, "Usage:")
		fmt.Fprintln(v.conn, p)
		fmt.Fprintln(v.conn, p, "\t /[cmd] [arguments]")
		fmt.Fprintln(v.conn, p)
		fmt.Fprintln(v.conn, p, "Available commands:")
		fmt.Fprintln(v.conn, p)
		fmt.Fprintln(v.conn, p, "\t/msg [to] [text]         send message to coworker")
		fmt.Fprintln(v.conn, p, "\t/list                    look at files you have access to")
		fmt.Fprintln(v.conn, p, "\t/send [to] [filename]    move file to coworker")
		fmt.Fprintln(v.conn, p, "\t/look                    show coworkers")
		//fmt.Fprint(v.conn, prompt)
	} else {
		log.Fatal("invalid sender id")
	}
}

func unknown(g GameState, msg *Message) {
	p := "    err -- | "
	if v, ok := g[msg.From]; ok {
		fmt.Fprintln(v.conn, p, "Invalid command try '/help' to see valid commands")
		//fmt.Fprint(v.conn, prompt)
	} else {
		log.Fatal("invalid sender id")
	}
}

func sendfile(g GameState, s *Snowden, msg *Message) {
	sender := g[msg.From]
	// file exists?
	if file, ok := sender.Files[msg.Msg]; ok {

		// recipient exists?
		if v, ok := g[msg.To]; ok {
			//delete
			delete(sender.Files, msg.Msg)
			//add file
			v.Files[msg.Msg] = file
			fmt.Fprintf(v.conn, "    send -- | Received File: %v(%v) from %v\n", msg.Msg, file.Size, msg.From)
			fmt.Fprintf(sender.conn, "    send -- | Sent File: %v to %v\n", msg.Msg, msg.To)
		} else {
			fmt.Fprintf(g[msg.From].conn, textWrap("error", " Invalid Recipient ID"))
		}

	} else {
		fmt.Fprintf(g[msg.From].conn, textWrap("error", " Invalid File ID"))
	}
	//fmt.Fprint(g[msg.From].conn, prompt)

}
func who(g GameState, s *Snowden, msg *Message) {
	if v, ok := g[msg.From]; ok {
		pre := "look --"
		fmt.Fprint(v.conn, textWrap(pre, "You look around at your co-workers' nametags:"))
		fmt.Fprint(v.conn, textWrap(pre, ""))
		for id, _ := range g {
			fmt.Fprint(v.conn, textWrap(pre, "\t"+id))
		}
		fmt.Fprint(v.conn, textWrap(pre, "\t"+s.String()))
		//fmt.Fprint(v.conn, prompt)
	} else {
		log.Fatal("recieved invalid sender ID")
	}

}

//Game commands: SendFile to player, sendfile to snowden, list files (director)
func gamedispatch(g GameState, s *Snowden, input <-chan *Message, errc chan<- error, totalTime <-chan time.Time) {
	var msg *Message
	for {
		select {
		case msg = <-input:
			if g[msg.From].isDone {
				continue
			}
			switch msg.Cmd {
			case CAST:
				broadcastmsg(g, msg)
			case MSG:
				if msg.To == s.String() {
					snowdendispatch(g, s, msg, errc)
				} else {
					sendmsg(g, msg)
				}
			case LIST:
				listfiles(g, msg)
			case SEND:
				if msg.To == s.String() {
					snowdendispatch(g, s, msg, errc)
				} else {
					sendfile(g, s, msg)
				}
			case HELP:
				help(g, msg)
			case WHO:
				who(g, s, msg)
			case ERR:
				unknown(g, msg)
			default:
			}
		case <-time.After(TurnTimeout):
			var conns []io.ReadWriteCloser

			for _, leaker := range g {
				conns = append(conns, leaker.conn)
			}
			broadcast(conns, GroupTimeoutString)
			errc <- fmt.Errorf("Turn Timeout")
			return

		case <-totalTime:
			var conns []io.ReadWriteCloser

			for _, leaker := range g {
				conns = append(conns, leaker.conn)
			}
			broadcast(conns, GroupTimeoutString)
			errc <- fmt.Errorf("Total Timeout")
			return
		}
	}
}

var (
	GroupTimeoutString = textWrap("fail", "You wake up bleary eyed and alone in a concrete box. Your head has a lump on the side. It seems corporate security noticed you didn't belong, you should have acted faster. You wonder if you will ever see your burrow again")
)

func textWrap(prefix, s string) string {
	const preSize = 10
	const spaces = "                   "
	buf := bytes.NewBuffer(make([]byte, 0, len(s)+preSize+5))
	padding := 0

	if len(prefix) < 10 {
		padding = 10 - len(prefix)
	}

	wrappedS := strings.Split(WrapString(s, 72), "\n")
	for i := range wrappedS {
		buf.WriteString(spaces[:padding])
		buf.WriteString(prefix)
		buf.WriteString(" | ")
		buf.WriteString(wrappedS[i])
		buf.WriteString("\n")
	}
	return buf.String()
}

func snowdendispatch(g GameState, s *Snowden, msg *Message, errc chan<- error) {
	if !s.Init && msg.Cmd == MSG {
		sendGreeting(g, s, msg)
		s.Init = true
	} else if msg.Cmd == MSG {
		fmt.Fprintf(g[msg.From].conn, textWrap("msg --", fmt.Sprintf("msg to %v: %v\n", msg.To, msg.Msg)))
		if strings.ToLower(msg.Msg) == "done" {
			g[msg.From].isDone = true

			alldone := true
			for _, v := range g {
				if !v.isDone {
					alldone = false
				}
			}
			if alldone {
				select {
				case <-s.Endtoken:
					endgame(g, s, msg, errc)
				default:
					return
				}
			} else {
				fmt.Fprintf(g[msg.From].conn, textWrap(s.String(), "Waiting for the rest of your team..."))
			}

		} else {
			fmt.Fprintf(g[msg.From].conn, textWrap(s.String(), "Are you finished sending me files? If so just say 'done'"))
		}
	} else if msg.Cmd == SEND {
		transfer(g, s, msg, errc)
	}
}

var ExceededLimit = textWrap("fail", "Oops, you got flagged for transfering too much data outside your group. Turns out giving away those files wasn't authorized. You and your friend Glenda are sent to your employer's overseas correctional training facility.")

func transfer(g GameState, s *Snowden, msg *Message, errc chan<- error) {
	sender := g[msg.From]
	// file exists?
	if file, ok := sender.Files[msg.Msg]; ok {
		delete(sender.Files, msg.Msg)
		flist := s.Files[msg.From]
		// enough bandwidth?
		g[msg.From].Bandwidth -= file.Size
		if sender.Bandwidth < 0 {
			//Exceeded Limit! You've been caught!
			fmt.Fprintf(sender.conn, ExceededLimit)
			errc <- fmt.Errorf("Transfer Quota Exceeded")
			return
		} else {
			//add file
			//flist = append(flist, file)
			s.Files[msg.From] = append(flist, file)
			fmt.Fprintf(sender.conn, textWrap("send --", fmt.Sprintf("Sent File: %v to %v", msg.Msg, msg.To)))
		}
	} else {
		fmt.Fprintf(g[msg.From].conn, textWrap("error", "Invalid File ID"))
	}
}
func endgame(g GameState, s *Snowden, msg *Message, errc chan<- error) {
	var sum = 0
	fmt.Fprintf(g[msg.From].conn, "    End -- | Exfiltrated files:\n")
	fmt.Fprintf(g[msg.From].conn, "    End -- |\n")

	w := new(tabwriter.Writer)
	w.Init(g[msg.From].conn, 4, 0, 1, ' ', tabwriter.AlignRight)

	fmt.Fprint(w, "  End -- | \tName\tSize\tSecrecy Value\t\n")

	for _, filelist := range s.Files {
		for i := range filelist {
			f := filelist[i]
			fmt.Fprintf(w, "   End -- | \t%v\t%vKB\t%v\t\n", f.Name, f.Size, f.Value)
			sum += f.Value
		}
	}
	w.Flush()

	fmt.Fprintf(g[msg.From].conn, "    End -- | \n")
	fmt.Fprintf(g[msg.From].conn, "    End -- | Total Document Impact Score: %v\n", sum)
	fmt.Fprintf(g[msg.From].conn, "    End -- | \n")
	if sum > s.SnowdenSolution {
		//win
		winStrs := []string{fmt.Sprintf("Congratulations gopher team! The total value of your documents has exceeded my target goal of %v.", s.SnowdenSolution)}
		winStrs = append(winStrs, "")
		winStrs = append(winStrs, "I'll need you to rendevous with our undercover agent at the GopherCon CoreOS booth. Our agent will help you escape the country before these leaks go live. Just whisper this secret phrase at the CoreOS booth to alert the agent. No one will suspect a thing!")
		winStrs = append(winStrs, "")
		winStrs = append(winStrs, "Secret Phrase: Peanut butter is the best.")

		for i := range winStrs {
			fmt.Fprintf(g[msg.From].conn, textWrap("Glenda", winStrs[i]))
		}
		log.Println("Documents exfiltrated successfully!")
	} else {
		endPhrase := []string{fmt.Sprintf("The total value of your transferred documents must exceed Glenda's target goal of: %v\n", s.SnowdenSolution)}
		endPhrase = append(endPhrase, "It seems the political impact of the exfiltrated documents is less then desirable. As a result our brave young gopher agents have trouble seeking asylum in neighboring soverign burrows.")
		endPhrase = append(endPhrase, "")
		endPhrase = append(endPhrase, "After leaking the document Glenda and the secret agent gophers are swiftly dealt with for their transgressions against The Agency... Oops. Better luck next life!")

		for i := range endPhrase {
			fmt.Fprintf(g[msg.From].conn, textWrap("fail", endPhrase[i]))
		}
		log.Println("Unsuccessful exfiltration")
	}
	errc <- fmt.Errorf("Game Finished")
}

type Solution struct {
	v, w int
	qty  []int
}

func solveknapsack(g GameState) int {
	value := 0
	for _, fileset := range g {
		files := make([]File, 0)
		//convert map to list
		for _, file := range fileset.Files {
			files = append(files, file)
		}
		v, _, s := choose(fileset.Bandwidth, len(files)-1, make(map[string]*Solution), files)

		//fmt.Println("Solution: ", id)
		for _, t := range s {
			if t > 0 {
				//fmt.Printf("  %d of %d %s\n", t, files[i].qty, files[i].Name)
			}
		}
		//fmt.Printf("Value: %d; bandwidth: %d\n", v, w)
		value += v
	}
	return value
}
func choose(bandwidth, pos int, cache map[string]*Solution, files []File) (int, int, []int) {

	if pos < 0 || bandwidth <= 0 {
		return 0, 0, make([]int, len(files))
	}

	str := fmt.Sprintf("%d,%d", bandwidth, pos)
	if s, ok := cache[str]; ok {
		return s.v, s.w, s.qty
	}

	best_v, best_i, best_w, best_sol := 0, 0, 0, []int(nil)

	for i := 0; i*files[pos].Size <= bandwidth && i <= files[pos].qty; i++ {
		v, w, sol := choose(bandwidth-i*files[pos].Size, pos-1, cache, files)
		v += i * files[pos].Value
		if v > best_v {
			best_i, best_v, best_w, best_sol = i, v, w, sol
		}
	}

	taken := make([]int, len(files))
	copy(taken, best_sol)
	taken[pos] = best_i
	v, w := best_v, best_w+best_i*files[pos].Size

	cache[str] = &Solution{v, w, taken}

	return v, w, taken
}

func sendGreeting(g GameState, s *Snowden, m *Message) {
	msg := []string{"Psst, hey there. I'm going to need your help if we want to exfiltrate these documents. You have clearance that I don't."}
	msg = append(msg, "")
	msg = append(msg, "You each have access to a different set of sensitive files. Within your group you can freely send files to each other for further analysis.  However, when sending files to me, the corporate infrastructure team will be alerted if you exceed your transfer quota. Working on too many files will make them suspicious.")
	msg = append(msg, "")
	msg = append(msg, "Please optimize your transfers by the political impact it will create without exceeding any individual transfer quota. The file's security clearance is a good metric to go by for that. Thanks!")
	msg = append(msg, "")
	msg = append(msg, "When each of you is finished sending me files, send me the message 'done'. I'll wait to hear this from all of you before we execute phase two.")

	for i := range msg {
		fmt.Fprintf(g[m.From].conn, textWrap(s.String(), msg[i]))
	}
}

// WrapString wraps the given string within lim width in characters.
//
// Wrapping is currently naive and only happens at white-space. A future
// version of the library will implement smarter wrapping. This means that
// pathological cases can dramatically reach past the limit, such as a very
// long word.
func WrapString(s string, lim uint) string {
	// Initialize a buffer with a slightly larger size to account for breaks
	init := make([]byte, 0, len(s))
	buf := bytes.NewBuffer(init)

	var current uint
	var wordBuf, spaceBuf bytes.Buffer

	for _, char := range s {
		if char == '\n' {
			if wordBuf.Len() == 0 {
				if current+uint(spaceBuf.Len()) > lim {
					current = 0
				} else {
					current += uint(spaceBuf.Len())
					spaceBuf.WriteTo(buf)
				}
				spaceBuf.Reset()
			} else {
				current += uint(spaceBuf.Len() + wordBuf.Len())
				spaceBuf.WriteTo(buf)
				spaceBuf.Reset()
				wordBuf.WriteTo(buf)
				wordBuf.Reset()
			}
			buf.WriteRune(char)
			current = 0
		} else if unicode.IsSpace(char) {
			if spaceBuf.Len() == 0 || wordBuf.Len() > 0 {
				current += uint(spaceBuf.Len() + wordBuf.Len())
				spaceBuf.WriteTo(buf)
				spaceBuf.Reset()
				wordBuf.WriteTo(buf)
				wordBuf.Reset()
			}

			spaceBuf.WriteRune(char)
		} else {

			wordBuf.WriteRune(char)

			if current+uint(spaceBuf.Len()+wordBuf.Len()) > lim && uint(wordBuf.Len()) < lim {
				buf.WriteRune('\n')
				current = 0
				spaceBuf.Reset()
			}
		}
	}

	if wordBuf.Len() == 0 {
		if current+uint(spaceBuf.Len()) <= lim {
			spaceBuf.WriteTo(buf)
		}
	} else {
		spaceBuf.WriteTo(buf)
		wordBuf.WriteTo(buf)
	}

	return buf.String()
}
