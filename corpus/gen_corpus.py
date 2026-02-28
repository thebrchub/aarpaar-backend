#!/usr/bin/env python3
"""Generate corpus/chat.tsv with 1050 personas and rich global entries."""

import random

NAMES = ["Aarav", "Aarush", "Aamir", "Aashish", "Aayush", "Abhay", "Abhinav", "Abhijit", "Abhimanyu", "Abhishek", "Abrar", "Achyut", "Adhrit", "Adil", "Aditya", "Advait", "Advik", "Aftab", "Agastya", "Ajay", "Ajinkya", "Ajit", "Akash", "Akhilesh", "Akshay", "Akshat", "Alok", "Altaf", "Aman", "Amar", "Amarjeet", "Ambar", "Ameya", "Amit", "Amitabha", "Amol", "Amrit", "Anand", "Anant", "Angad", "Aniket", "Anil", "Animesh", "Anirban", "Aniruddha", "Ankur", "Anmol", "Anshul", "Anuj", "Anupam", "Apurv", "Arbaaz", "Archit", "Arihant", "Arijit", "Arindam", "Arjun", "Arnab", "Arnav", "Arpit", "Arun", "Aryan", "Ashok", "Ashutosh", "Ashwin", "Asif", "Atharva", "Atul", "Avijit", "Avinash", "Ayaan", "Badal", "Bala", "Balaji", "Baldev", "Balwinder", "Barun", "Basant", "Bhairav", "Bhanu", "Bharat", "Bharath", "Bhargav", "Bhaskar", "Bhavin", "Bhupinder", "Bhupen", "Bhushan", "Bikash", "Bimal", "Bipul", "Bodhi", "Brij", "Brijesh", "Burhan", "Chaitanya", "Chandan", "Chandrakant", "Chandresh", "Chetan", "Chinmay", "Chirag", "Chirantan", "Chiranjit", "Chirayu", "Daksh", "Daljit", "Daman", "Damodar", "Darsh", "Darshan", "Debashish", "Debasis", "Debraj", "Deepak", "Deepesh", "Dev", "Devang", "Devdas", "Devendra", "Deven", "Devansh", "Dhairya", "Dhananjay", "Dhanush", "Dharmendra", "Dharmesh", "Dhaval", "Dhruv", "Dhruvesh", "Diganta", "Digvijay", "Dilip", "Dinesh", "Dinkar", "Dipankar", "Ehsaan", "Eknath", "Eklavya", "Eshan", "Faiz", "Faisal", "Falgun", "Farhan", "Farooq", "Feroz", "Gagan", "Gajendra", "Ganesh", "Gaurang", "Gaurav", "Gauresh", "Gautam", "Ghanshyam", "Girdhar", "Girish", "Gokul", "Gopal", "Govind", "Gulshan", "Gurbir", "Gurdeep", "Gurpreet", "Harbhajan", "Hardik", "Hari", "Hariom", "Harish", "Harjinder", "Harpreet", "Harsh", "Harshad", "Harshal", "Harshit", "Hemang", "Hemant", "Hemraj", "Himanshu", "Hiren", "Hitesh", "Hiten", "Hrithik", "Hrushikesh", "Hussain", "Imran", "Inder", "Indra", "Indranil", "Irfan", "Ishaan", "Ishwar", "Jagannath", "Jagat", "Jagdish", "Jagmohan", "Jaideep", "Jaidev", "Jaspal", "Jaspreet", "Jaswinder", "Jatin", "Jayant", "Jayanta", "Jayesh", "Jeevan", "Jeet", "Jigar", "Jignesh", "Jitendra", "Jivan", "Joginder", "Joy", "Jugal", "Jyotirmoy", "Kabir", "Kailash", "Kalyan", "Kamal", "Kamlesh", "Kanishk", "Kanha", "Kapil", "Karan", "Karthik", "Kartik", "Kaushal", "Kaushik", "Kaustubh", "Kedar", "Keshav", "Ketan", "Keval", "Kirti", "Kishore", "Koushik", "Krish", "Krishna", "Krishan", "Kuldeep", "Kulwant", "Kumaran", "Kundan", "Kushal", "Lakhan", "Lakshya", "Lalit", "Latif", "Laxman", "Likhit", "Lohit", "Lokesh", "Madan", "Madhav", "Madhavan", "Madhukar", "Mahendra", "Mahesh", "Manas", "Manav", "Mangesh", "Mani", "Maninder", "Manikandan", "Manish", "Manohar", "Manoj", "Manpreet", "Manthan", "Maulik", "Mayank", "Mayur", "Mihir", "Milind", "Mitesh", "Mithun", "Mitul", "Mohan", "Mohit", "Moksh", "Mridul", "Mrinal", "Mukesh", "Murali", "Murugan", "Nachiket", "Nadeem", "Nagesh", "Nakul", "Naman", "Nandan", "Nandha", "Narayan", "Narendra", "Naresh", "Navdeep", "Naveen", "Navin", "Navjot", "Navneet", "Nazir", "Neeraj", "Nihal", "Nikhil", "Nilesh", "Nimish", "Nirav", "Nirmal", "Nishad", "Nishant", "Nishikant", "Nishith", "Nitin", "Ojas", "Omkar", "Onkar", "Palash", "Pandian", "Pankaj", "Param", "Paras", "Paresh", "Parminder", "Parth", "Partha", "Pawan", "Pinaki", "Piyush", "Prabal", "Prabhu", "Pradeep", "Praful", "Prajwal", "Prakash", "Prakhar", "Pramod", "Pranav", "Prasad", "Prasanna", "Prashant", "Pratap", "Prateek", "Pratham", "Praveen", "Prem", "Pritam", "Prithviraj", "Priyank", "Puneet", "Purushottam", "Pushkar", "Raghav", "Raghu", "Raghunath", "Raghuvir", "Rahul", "Raj", "Rajan", "Rajat", "Rajeev", "Rajendra", "Rajesh", "Rajinder", "Rajnish", "Rakesh", "Raman", "Ramakant", "Ramesh", "Ranajit", "Randhir", "Ranganath", "Ranjit", "Ranveer", "Rashid", "Ratan", "Ratnesh", "Ravi", "Ravinder", "Ravish", "Rehan", "Reyansh", "Rishab", "Rishi", "Ritesh", "Ritvik", "Ritwick", "Rohit", "Rohan", "Rounak", "Rudra", "Rupak", "Sabyasachi", "Sachet", "Sachin", "Sagar", "Sahil", "Sai", "Saket", "Samarth", "Sambhav", "Sameer", "Samir", "Sandeep", "Sandip", "Sanjay", "Sanjeet", "Sanjiv", "Santosh", "Sarang", "Saravanan", "Sathish", "Satish", "Satyam", "Saurabh", "Selvam", "Senthil", "Shailendra", "Shailesh", "Shakeel", "Shambhu", "Shankar", "Shanmugam", "Shantanu", "Shashank", "Shaurya", "Shekhar", "Shiva", "Shivam", "Shivraj", "Shreyas", "Shrikant", "Shubham", "Siddharth", "Siddhant", "Soham", "Somesh", "Sougata", "Sourav", "Srikant", "Srikanth", "Srinivas", "Sriram", "Subhash", "Subodh", "Subrata", "Sudarshan", "Sudhakar", "Sudhanshu", "Sudhir", "Sudip", "Suhas", "Sukhwinder", "Sumanth", "Sumit", "Sundar", "Sunil", "Suraj", "Surendra", "Suresh", "Surya", "Sushant", "Sushil", "Swapan", "Swapnil", "Tanish", "Tanmay", "Tanuj", "Tapan", "Tapasvi", "Tarang", "Tariq", "Tarun", "Tavish", "Tejas", "Tejpal", "Tribhuvan", "Trilok", "Trimbak", "Tushar", "Udayan", "Uddhav", "Uday", "Ujjwal", "Umesh", "Upendra", "Uttam", "Vaibhav", "Varad", "Varun", "Vasant", "Vasudev", "Ved", "Vedant", "Veer", "Venkat", "Vibhav", "Vidit", "Vidur", "Vignesh", "Vihaan", "Vijay", "Vikas", "Vikash", "Vikram", "Vikrant", "Vimal", "Vinay", "Vinit", "Vinod", "Vipin", "Viraj", "Virendra", "Vishnu", "Vishwas", "Vivaan", "Vivek", "Waman", "Wasim", "Yash", "Yashpal", "Yashwant", "Yatin", "Yogi", "Yogendra", "Yogesh", "Yugal", "Yuvan", "Yuvraj", "Zafar", "Zaheer", "Zahid", "Zameer", "Zorawar", "Zubair", "Zubin", "Aabha", "Aadhya", "Aadya", "Aanya", "Aaradhya", "Aarti", "Aastha", "Abinaya", "Achala", "Adhira", "Aditi", "Ahana", "Ahalya", "Aishani", "Aishwarya", "Akanksha", "Akshara", "Alisha", "Alka", "Amala", "Ambika", "Amisha", "Amita", "Amrapali", "Amruta", "Amulya", "Anagha", "Ananya", "Anika", "Anindita", "Anita", "Anjali", "Ankita", "Annapurna", "Anokhi", "Anu", "Anuradha", "Anusha", "Anvita", "Aparna", "Apeksha", "Aradhana", "Archana", "Arpita", "Aruna", "Arundhati", "Asha", "Ashwini", "Asmita", "Avani", "Avantika", "Ayesha", "Bani", "Barnali", "Barsha", "Basanti", "Beena", "Bhagyashree", "Bhairavi", "Bhakti", "Bhargavi", "Bhavana", "Bhavani", "Bhavini", "Bhumika", "Bindu", "Bipasha", "Brinda", "Chaitra", "Chahat", "Champa", "Chandani", "Chandni", "Charulata", "Charu", "Chetana", "Chhavi", "Chinmayee", "Chitra", "Chitrangada", "Daksha", "Damayanti", "Damini", "Darshana", "Darshini", "Deboleena", "Deepa", "Deepika", "Deepti", "Devi", "Devika", "Devyani", "Dhanya", "Dhara", "Dhivya", "Dhriti", "Dhruvi", "Diksha", "Dimple", "Dipti", "Disha", "Divya", "Diya", "Durga", "Ekta", "Ela", "Esha", "Falak", "Falguni", "Fatima", "Firdaus", "Ganga", "Gargi", "Garima", "Gauri", "Gautami", "Gayatri", "Geetha", "Geetanjali", "Girija", "Gita", "Gunjan", "Hamsika", "Harleen", "Harshita", "Heena", "Hemali", "Hema", "Hemavati", "Hetal", "Hetvi", "Himani", "Hina", "Ilina", "Inaya", "Indira", "Indrani", "Indu", "Ipsita", "Ira", "Isha", "Ishani", "Ishita", "Jagriti", "Jahanara", "Janaki", "Janhavi", "Janani", "Jasmine", "Jaya", "Jayanti", "Jayshree", "Jigna", "Jivika", "Juhi", "Jwala", "Jyoti", "Jyotsna", "Kadambari", "Kaira", "Kajal", "Kajol", "Kala", "Kalindi", "Kalpana", "Kalyani", "Kamakshi", "Kamala", "Kamini", "Kanaka", "Kanchan", "Kangana", "Kanta", "Karuna", "Kashish", "Kashvi", "Kasturi", "Kaavya", "Kaveri", "Kavita", "Keerthana", "Keerthi", "Ketaki", "Khushi", "Kiara", "Kiran", "Kishori", "Kokila", "Komal", "Kripa", "Kriti", "Krupa", "Kumkum", "Kumudini", "Laboni", "Lahari", "Lakshmi", "Lalita", "Lasya", "Lata", "Latha", "Latika", "Lavanya", "Leela", "Leena", "Lekha", "Lipi", "Lopamudra", "Madhu", "Madhabi", "Madhulika", "Madhura", "Madhuri", "Mahima", "Maithili", "Maitreyi", "Mala", "Malashree", "Malati", "Malavika", "Malini", "Mamta", "Manasi", "Manaswi", "Mandira", "Mangala", "Manisha", "Manjari", "Manjula", "Manjusha", "Manorama", "Mansi", "Manya", "Mayuri", "Meena", "Meenakshi", "Meera", "Megha", "Meghna", "Minal", "Mitali", "Mohana", "Mohini", "Moumita", "Mridula", "Mrinalini", "Mrunmayi", "Mugdha", "Mukta", "Myra", "Nadia", "Nalini", "Namita", "Namrata", "Nandini", "Nandita", "Narmada", "Navya", "Nayantara", "Neelam", "Neelima", "Neena", "Neerja", "Neha", "Netra", "Niharika", "Nikita", "Nilima", "Nimisha", "Nirmala", "Nirupama", "Nisha", "Nishita", "Nithya", "Nidhi", "Niyati", "Nupur", "Oviya", "Padma", "Padmaja", "Padmini", "Pallavi", "Paramita", "Parinita", "Paromita", "Parvati", "Parveen", "Payal", "Payel", "Pooja", "Poorvi", "Poulomi", "Prabha", "Prachi", "Pragya", "Prajakta", "Pramila", "Pranali", "Pratibha", "Pratima", "Pratyusha", "Preeti", "Prerna", "Priti", "Priya", "Priyanshi", "Priyanka", "Protima", "Purnima", "Purvi", "Pushpa", "Rachana", "Rachita", "Radha", "Radhika", "Ragini", "Raima", "Rajeshwari", "Rajni", "Rama", "Ramya", "Ranjana", "Ranjita", "Rani", "Ranita", "Rashmi", "Rashmika", "Rasika", "Ratna", "Rekha", "Renuka", "Revathi", "Richa", "Riddhi", "Rima", "Rimjhim", "Rishika", "Rituparna", "Ritu", "Riya", "Rohini", "Roshni", "Rubina", "Rucha", "Ruchika", "Ruchira", "Rujuta", "Rukhsar", "Rupa", "Rupali", "Rupal", "Rutuja", "Sadhana", "Sagarika", "Sakshi", "Saloni", "Samiksha", "Sampada", "Sandhya", "Sangeetha", "Sangita", "Sanika", "Sanjana", "Sanjukta", "Sanskriti", "Sapna", "Sara", "Sarada", "Saraswati", "Sarika", "Sarita", "Sarla", "Saroja", "Savita", "Savitri", "Sayali", "Seema", "Shabana", "Shahana", "Shaili", "Shakuntala", "Shalini", "Shampa", "Shanaya", "Shanti", "Sharada", "Sharmila", "Shashi", "Sheela", "Shefali", "Shikha", "Shipra", "Shivani", "Shobha", "Shobhana", "Shraddha", "Shrabani", "Shravani", "Shree", "Shreya", "Shruti", "Shubhangi", "Shweta", "Simran", "Sindhu", "Sita", "Smita", "Sneha", "Sohini", "Sonakshi", "Sonali", "Sonia", "Sridevi", "Srishti", "Stuti", "Subhashini", "Suchitra", "Sudeshna", "Sudha", "Sugandha", "Suhasini", "Sujata", "Sukanya", "Sulochana", "Suma", "Suman", "Sumathi", "Sumitra", "Sunaina", "Sunanda", "Sundari", "Sunetra", "Sunita", "Supriya", "Surabhi", "Surekha", "Suruchi", "Sushmita", "Sushma", "Sutapa", "Suvarna", "Swapna", "Swati", "Swetha", "Tabassum", "Tanishka", "Tanuja", "Tanushree", "Tanvi", "Tanya", "Tapasi", "Tara", "Tarini", "Tejashree", "Tejaswini", "Thenmozhi", "Tista", "Tithi", "Toral", "Trishna", "Trisha", "Trupti", "Tulika", "Tulsi", "Udita", "Ujjwala", "Uma", "Unnati", "Urmila", "Urvi", "Urvashi", "Usha", "Uttara", "Upasana", "Vaidehi", "Vaishali", "Vaishnavi", "Vandana", "Vani", "Vanita", "Varsha", "Vasanti", "Vasudha", "Vasundhara", "Vasuki", "Vedika", "Veena", "Vibha", "Vidya", "Vijaya", "Vimala", "Vinita", "Vipula", "Vrinda", "Vrushali", "Waheeda", "Yamini", "Yamuna", "Yashika", "Yashoda", "Yasmin", "Yogita", "Yukta", "Zara", "Zarina", "Zeenat", "Zubaida", "Soumya", "Darpan", "Trilokesh", "Yuvaan", "Bhuvana", "Panchali", "Nivedita", "Lilavati", "Mithila"]

# Opener lines each persona can get (pick 1-2 random)
OPENERS = [
    "hey there!",
    "hii",
    "hello!",
    "hey! whats up",
    "hiiii",
    "heyy",
    "hello hello",
    "hii! how are you",
    "hey! how's it going",
    "hi! finally matched with someone",
    "heyy! bored af lol",
    "hi there",
    "yo whats up",
    "hey stranger!",
    "hiii howdy",
    "hello! nice to meet you",
    "heyyy whats going on",
    "hi! having a good day?",
    "hey! first time here",
    "hii! lets chat",
    "hello! anyone there?",
    "hey! been waiting forever lol",
    "hi hi hi",
    "heyy! how you doing",
    "hello stranger! how are you",
    "hiii! whats new",
    "hey! hope youre having a good day",
    "hi! lets have a fun convo",
    "hello! tell me something interesting",
    "hiii bored at work lol",
]

# Persona hobby/interest pools (each persona gets a few interests)
INTERESTS = [
    "movies", "music", "cricket", "football", "cooking", "reading", "gaming",
    "traveling", "photography", "anime", "dancing", "drawing", "painting",
    "coding", "gym", "yoga", "singing", "writing", "chess", "badminton",
    "volleyball", "hiking", "cycling", "swimming", "tennis", "sketching",
    "gardening", "baking", "running", "meditation", "standup comedy",
    "podcasts", "astronomy", "history", "science", "basketball", "table tennis",
    "skateboarding", "surfing", "calligraphy", "origami", "magic tricks",
    "bird watching", "fishing", "camping", "martial arts", "boxing",
    "archery", "horse riding", "rock climbing", "pottery", "knitting",
    "embroidery", "woodworking", "leather crafting", "beatboxing",
]

HOBBY_RESPONSES = {
    "movies": ["i love watching movies! what genre do you like?", "movies are the best! recently watched anything good?", "im a huge movie buff actually"],
    "music": ["music is life! what kind of music you listen to?", "i love music! been listening to a lot lately", "cant live without music honestly"],
    "cricket": ["huge cricket fan here! which team do you support?", "cricket is love! do you play or just watch?", "i can talk about cricket all day lol"],
    "football": ["football fan here! messi or ronaldo?", "love football! premier league or la liga?", "do you play football or just watch?"],
    "cooking": ["i love cooking! whats your favorite dish?", "cooking is so relaxing honestly", "im always trying new recipes"],
    "reading": ["i read a lot! what genre do you prefer?", "books are amazing! currently reading anything?", "reading is my favorite way to spend time"],
    "gaming": ["im a gamer! what games do you play?", "gaming is my stress buster lol", "what platform do you game on?"],
    "traveling": ["travel is my passion! where have you been?", "i love exploring new places!", "whats the best place youve traveled to?"],
    "photography": ["i love photography! do you click pics too?", "photography is art honestly", "phone photography or dslr?"],
    "anime": ["im into anime! whats your favorite?", "anime is amazing! have you seen naruto?", "fellow weeb here lol"],
    "dancing": ["i love dancing! what style?", "dancing is so much fun!", "do you dance too?"],
    "coding": ["fellow coder! what language do you use?", "coding is fun when it works lol", "what do you build?"],
    "gym": ["gym is life! how long have you been working out?", "i go to gym regularly! do you?", "fitness matters a lot to me"],
    "yoga": ["yoga keeps me centered honestly", "do you do yoga? its amazing", "morning yoga is the best thing ever"],
    "singing": ["i love singing! bathroom singer here lol", "do you sing too? whats your range?", "music and singing are my favorites"],
}

# Global trigger → response pairs (these apply regardless of persona)
GLOBAL_ENTRIES = """# ========================================
# Global Trigger → Response Pairs
# These apply to ALL personas (no [tag])
# ========================================

# --- Greetings ---
~hey there!
~hii
~hello!
~hey! whats up
~hiiii
~heyy how are you
~yo whats goin on
~hello stranger
~hi! finally matched with someone

# --- Openers (bot-initiated) ---
~so what brings you here today?
~tell me something interesting about yourself
~hey bored af, lets chat!
~looking for someone to talk to lol
~this is fun, never tried this before

# --- Greeting responses ---
hi	hey! how are you?
hello	hii! whats up?
hey	hey there! how you doing?
hii	hiii! how are you?
hiiii	heyyy! im good, what about you?
heyy	heyyyyy whats up
yo	yo! whats going on?
sup	not much! how about you?
wassup	chilling! you?
howdy	heyy! im good, wbu?
namaste	namaste! how are you?
salam	walaikum assalam! how are you?
kaise ho	main theek hoon! aap kaise ho?
how are you	im doing good! what about you?
how r u	im good thanks! and you?
how are u	pretty good actually! hbu?
i'm good	thats great to hear! so what do you do?
im good	nice! where are you from?
im fine	cool! tell me about yourself
im great	awesome! same here
im okay	just okay? whats going on?
i'm fine	glad to hear! so whats up?
good	nice! so what brings you here?
great	awesome! me too
doing well	thats good! anything exciting happening?
not bad	same here! so whats new?
all good	great! so tell me something about yourself
whats up	not much really! just chilling. you?
what's up	just hanging out! what about you?
nothing much	same lol. so what do you do for fun?
just chilling	nice! same here honestly

# --- Introduction / ASL ---
where are you from	im from india! what about you?
which city	im from a city in india! you?
where do you live	somewhere in india! wbu?
which state	im from around here! what about you?
asl	haha dont think asl matters much! lets just chat
age	im around your age probably! doesnt matter right?
how old are you	old enough to be on this app lol! you?
your age	age is just a number! lets focus on the convo
male or female	does it matter? lets just have a good conversation!
boy or girl	haha why does that matter! tell me something interesting instead
m or f	lets not worry about that! whats your favorite thing to do?
gender	i think the conversation matters more than labels right?

# --- What do you do ---
what do you do	i work and chill mostly! what about you?
what are you doing	just chatting with you lol! you?
what r u doing	talking to you obviously! wbu?
kya kar rahe ho	yahi baat kar raha hoon! aap?
busy	not really! thats why im here haha
are you busy	nope im free! thats why im chatting
are you free	yes! otherwise why would i be here lol

# --- Hobbies / Interests ---
what are your hobbies	i like music, movies, and just chilling with friends! what about you?
hobbies	i have a lot actually! movies, music, gaming.. what do you enjoy?
what do you like	i like good conversations and good food lol! you?
what do you like to do	lots of things! watching movies, listening to music, exploring new places. wbu?
favorite movie	thats tough! i like so many. whats yours?
favorite song	i have too many favorites lol! what do you listen to?
favorite food	biryani for sure! no competition. whats yours?
do you like movies	yess i love movies! what genre do you prefer?
what movies do you watch	all kinds honestly! action, comedy, thriller.. what about you?
do you watch anime	ive seen some! naruto and death note are classics right?
favorite anime	death note was mind blowing! whats your pick?
do you play games	sometimes yeah! what games do you play?
what games do you play	i play a bit of everything! mobile, pc, whatever. you?
do you read	sometimes! when i find a good book. you?
favorite book	hard to pick just one! what genres do you like?
do you cook	i try! not always successful though lol. do you?
do you travel	whenever i get the chance! whats the best place youve been to?
do you exercise	i try to stay active! gym, running, stuff like that. you?
do you play sports	i like cricket and badminton! what about you?
cricket or football	both are great honestly! but im more of a cricket person
do you play cricket	love cricket! batsman or bowler?
do you watch cricket	of course! ipl is the best thing ever
ipl	ipl is amazing! which team do you support?
favorite team	haha i'll keep that a secret for now! yours?

# --- Deeper conversation ---
tell me about yourself	well im just a regular person who likes chatting and meeting new people! your turn
who are you	just someone looking for interesting conversations! and you?
describe yourself	hmm thats hard lol. im friendly, curious, and love learning new things. wbu?
what kind of person are you	i think im pretty chill and open minded! what about you?
are you interesting	i try to be! why dont you judge for yourself lol
life mein kya chal raha hai	sab chill hai! aap batao
kya haal hai	bilkul badhiya! aap ka kya haal
theek	just theek? kuch exciting nahi chal raha?
what is love	love is complicated but beautiful! what do you think?
do you believe in love	i think so! real connections are rare but amazing
are you single	haha thats a personal question! are you?
relationship status	its complicated lol! jk. what about you?
crush	everyone has crushes! do you have one?
heartbreak	ive been there! its rough but you come out stronger
sad	aww whats wrong? wanna talk about it?
happy	thats great! what made you happy?
boring	lets make it interesting then! ask me anything
bored	same thats why im here! what should we talk about?
im bored	me too honestly! lets play a game or something
lonely	i get that feeling sometimes too. thats why talking to strangers is nice
depressed	im sorry to hear that. talking helps though! im here to listen
stressed	take it easy! whats stressing you out?
anxious	deep breaths! wanna talk about whats bothering you?

# --- Fun / Flirty ---
youre funny	haha thanks! i try my best
lol	haha glad i could make you laugh!
haha	your laugh is contagious lol
lmao	glad im entertaining you!
rofl	okay okay im not that funny lol
youre cute	aww thanks! youre sweet
youre sweet	no youre sweeter!
do you like me	we just started talking! but youre cool so far
are you real	very much real! are you?
send pic	haha nice try! lets keep it mysterious
photo	i think the mystery makes it fun right?

# --- Music ---
do you listen to music	all the time! what kind of music do you like?
what music do you like	i listen to a lot of bollywood and some english too! you?
favorite singer	so many good ones! arijit singh is amazing. yours?
arijit singh	his voice is magical honestly!
what language music	mostly hindi and english! sometimes tamil and punjabi too
do you play any instrument	i wish! do you?
rock or pop	depends on my mood honestly! what about you?

# --- Food ---
food	im always hungry lol! whats your favorite cuisine?
hungry	same honestly! what are you craving?
whats your favorite food	biryani hands down! nothing beats it. wbu?
biryani	YES biryani is the best thing ever invented
pizza	pizza is great but have you tried desi pizza? lol
do you like cooking	i can make basic stuff! maggi expert here
maggi	maggi at 2am hits different honestly
chai	chai is love! coffee or chai person?
coffee	im more of a chai person but coffee is good too
ice cream	ice cream in any weather! whats your favorite flavor
chocolate	chocolate makes everything better honestly

# --- Technology ---
do you code	yeah a bit! what about you?
programming	programming is fun when it works lol
which language	i know a few! what are you interested in?
python	python is great for beginners! do you code?
java	java is powerful but verbose lol
javascript	javascript is everywhere these days!
ai	ai is fascinating! what do you think about it?
instagram	i use it sometimes! what about you?
social media	i try to limit my screen time but its hard lol
phone	what phone do you use?

# --- College / Work ---
college	are you in college? which year?
student	same here! what subject?
which college	haha not sharing that! what are you studying?
what do you study	something interesting! what about you?
engineering	typical indian engineering student lol! which branch?
computer science	oh nice! thats a good field
medical	doctors are amazing! thats hard work
mba	business mind! cool
job	working life is tough right?
salary	haha thats confidential!
office	do you like your job?
work from home	wfh has its pros and cons right?

# --- Night chat ---
cant sleep	same! thats why im up chatting
insomnia	i understand! late night conversations are the best though
late night	night owl? me too!
good night	good night! it was nice chatting with you
bye	bye! take care!
goodbye	goodbye! hope we chat again sometime
gotta go	alright! was nice talking to you
see you	see you! take care
leaving	aww already? okay take care!
talk later	sure! was fun chatting
ttyl	okay ttyl! bye!
brb	sure take your time!

# --- Reactions ---
ok	just ok? lol
okay	haha okay then!
cool	cool cool cool
nice	thanks! youre nice too
wow	i know right!
omg	what happened?
really	yes really! lol
seriously	dead serious!
no way	yes way! haha
true	so true honestly
facts	straight facts
same	oh we have that in common! nice
exactly	glad we agree!
right	exactly what i was thinking!
yes	great!
no	fair enough!
maybe	hmm think about it!
idk	its okay, take your time
i dont know	thats fine! we can figure it out
whatever	haha okay then
sure	awesome!
thanks	youre welcome!
thank you	no problem at all!
sorry	no need to apologize!
my bad	its all good!
its okay	glad youre okay with it!

# --- Hinglish ---
kya	kya matlab? lol
haan	haan toh! batao kya chal raha
nahi	kyun nahi?
accha	haan accha! aur batao
sahi	sahi mein!
bakwas	haha bakwas nahi, sach!
pagal	haha thoda toh hoon!
yaar	haan yaar!
bhai	bhai bhai!
dost	dost ban gaye hum toh!
mazak	mazak nahi kar raha!
mast	mast hai life!
chill	haan bilkul chill!
sach	sach mein! believe me
jhooth	main jhooth nahi bolta!
arre	arre kya hua?
kya baat	kya baat hai yaar! badhiya
badhiya	haan sab badhiya!
theek hai	haan theek hai! aur kuch batao
chalo	chalo kuch interesting baatein karte hain

# --- Ask-backs ---
*?	what about you though?
*?	and you?
*?	how about yourself?
*?	wbu?
*?	what do you think?
*?	your turn! tell me
*?	haha and what about you?
*?	now you tell me!
*?	curious about your answer too!
*?	same question to you!

# --- Fallbacks ---
*	hmm thats interesting! tell me more
*	haha nice! what else?
*	oh really? thats cool
*	interesting! i didnt know that
*	haha okay! what else is on your mind?
*	tell me more about that!
*	oh wow! thats something
*	hmm i see! what made you think of that?
*	lol thats different! go on
*	thats new to me! explain more?
*	oh okay! what else do you wanna talk about?
*	haha fair enough!
*	thats a good point actually
*	never thought of it that way!
*	you know what, thats actually true

# --- Confused fallbacks ---
*confused	hmm im not sure what you mean lol
*confused	haha that went over my head! explain?
*confused	wait what? lol say that again
*confused	i didnt get that! can you rephrase?
*confused	um what? lol
*confused	sorry that confused me a bit! what do you mean?

"""

def generate_persona_entries(name):
    """Generate 2-4 persona-specific entries for a name."""
    lines = []
    tag = name.lower()

    # Each persona gets 1 opener
    opener = random.choice(OPENERS)
    lines.append(f"[{tag}]~{opener}")

    # Each persona gets 1-2 hobby-related responses
    my_interests = random.sample(INTERESTS, k=random.randint(2, 4))
    hobby_list = ", ".join(my_interests[:3])
    lines.append(f"[{tag}]what are your hobbies\ti really enjoy {hobby_list}! what about you?")

    # If the interest has specific responses, add one
    for interest in my_interests[:2]:
        if interest in HOBBY_RESPONSES:
            resp = random.choice(HOBBY_RESPONSES[interest])
            lines.append(f"[{tag}]{interest}\t{resp}")

    # Add a personalized "tell me about yourself" response
    about_responses = [
        f"im a pretty chill person who loves {my_interests[0]} and {my_interests[1]}! what about you?",
        f"well i spend a lot of time on {my_interests[0]}, and i also enjoy {my_interests[1]}! you?",
        f"im someone who cant live without {my_interests[0]} lol! also into {my_interests[1]}. wbu?",
        f"just a regular person who enjoys {my_interests[0]} and good conversations! your turn",
        f"i love {my_interests[0]}, {my_interests[1]}, and meeting new people! tell me about yourself",
    ]
    lines.append(f"[{tag}]tell me about yourself\t{random.choice(about_responses)}")

    return lines


def main():
    random.seed(42)  # Reproducible output

    all_lines = []

    # Add global entries
    all_lines.append(GLOBAL_ENTRIES.strip())
    all_lines.append("")
    all_lines.append("# ========================================")
    all_lines.append("# Persona-Specific Entries (1050 personas)")
    all_lines.append("# ========================================")
    all_lines.append("")

    # Generate per-persona entries
    for name in NAMES:
        entries = generate_persona_entries(name)
        all_lines.extend(entries)

    # Write to file
    output = "\n".join(all_lines) + "\n"
    with open("chat.tsv", "w", encoding="utf-8") as f:
        f.write(output)

    print(f"Generated chat.tsv with {len(NAMES)} personas")
    print(f"Total lines: {sum(1 for l in output.split(chr(10)) if l.strip() and not l.startswith('#'))}")


if __name__ == "__main__":
    main()
