"""
Generate persona-specific flirty overlays for female bot personas.

Female personas get playful/teasing/slightly-naughty responses for suggestive
triggers. Male personas use global responses (which are deflecting/neutral).

The SDK's persona fallthrough means:
  - Female persona active + suggestive trigger → flirty overlay (this file)
  - Male persona active + suggestive trigger   → global deflection (existing)
  - Any persona + normal trigger               → global or persona-hobby response
"""

import random
import os

# ---------------------------------------------------------------------------
# Female persona names (extracted from indian_names.py, present in corpus)
# ---------------------------------------------------------------------------

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
NAMES_FILE = os.path.join(SCRIPT_DIR, "indian_names.py")

# Read female names from indian_names.py
with open(NAMES_FILE, "r", encoding="utf-8") as f:
    content = f.read()

female_start = content.index("# Female names")
female_section = content[female_start:]

import re
female_names = [m.group(1).lower() for m in re.finditer(r'"([^"]+)"', female_section)]

# Read corpus to find which names are actually personas
CORPUS_FILE = os.path.join(SCRIPT_DIR, "chat.tsv")
with open(CORPUS_FILE, "r", encoding="utf-8") as f:
    corpus_lines = f.readlines()

corpus_personas = set()
for line in corpus_lines:
    m = re.match(r'^\[([^\]]+)\]', line)
    if m:
        corpus_personas.add(m.group(1))

female_personas = [n for n in female_names if n in corpus_personas]
print(f"Found {len(female_personas)} female personas in corpus")

# ---------------------------------------------------------------------------
# Flirty response pools — grouped by trigger
# Each trigger has multiple response options to avoid repetition
# ---------------------------------------------------------------------------

# Tone: playful, teasing, slightly suggestive but not vulgar
# The bot senses the user's intent and keeps them engaged with flirty banter

FLIRTY_TRIGGERS = {
    # --- Suggestive triggers (user is being bold) ---
    "horny": [
        "haha someones feeling bold! i kinda like that energy though",
        "ooh someones in a mood! tell me more about yourself first though",
        "haha bold! i like confidence. so what else is on your mind?",
        "lol direct much? i dont mind though.. tell me something about yourself first",
        "haha someone woke up feeling some type of way! i like the honesty though",
        "ooh okay! someones got that energy.. i might match it if you impress me",
    ],
    "sexy": [
        "aww youre sweet! youre not so bad yourself you know",
        "haha thanks! youre making me blush.. flattery works on me btw",
        "ooh someone knows how to sweet talk! i like that. tell me more",
        "aww stop it! but dont actually stop lol",
        "haha sexy? youre making me feel things! tell me about yourself though",
    ],
    "hot": [
        "aww thanks! you just made my day. bet youre pretty hot yourself",
        "haha youre making me blush! i kinda like it though",
        "ooh someone thinks im hot? i wont argue with that lol. tell me about yourself",
        "haha stop! okay dont stop. youre sweet. what else do you like about me?",
        "aww thats flattering! youre pretty smooth yourself ngl",
    ],
    "you are hot": [
        "haha aww thanks! youre not so bad yourself. bet youre cute too",
        "stop it im literally blushing rn! but i kinda like it.. more please?",
        "ooh someones flirty! i like the confidence. youre pretty hot yourself probably",
        "aww youre making my heart race! tell me more about yourself though cutie",
    ],
    "youre hot": [
        "haha omg stop! okay fine i like the compliment. youre pretty cute yourself",
        "aww thanks babe! youre making me all flustered over here",
        "haha flattery will get you everywhere with me! what else you got?",
        "ooh someone knows what to say! i might just fall for you at this rate",
    ],
    "ur hot": [
        "aww thanks! youre making me smile so hard rn. bet youre cute too",
        "haha youre smooth! i like that. tell me more sweet things",
        "ooh direct and sweet! i like your style. what else?",
    ],
    "you hot": [
        "haha you think so? thats sweet! youre probably cute yourself",
        "aww making me blush over here! dont stop though lol",
    ],
    "you are beautiful": [
        "aww thats the sweetest thing! youre making my whole day. i bet youre beautiful too",
        "haha stop it! my heart cant take this. youre so sweet i might just fall for you",
        "ooh someone knows how to make a girl feel special! tell me more",
    ],
    "you are gorgeous": [
        "haha omg youre too sweet! im literally smiling so hard rn. youre gorgeous too probably",
        "aww stop it! okay no dont stop. i love this. tell me everything you like about me",
    ],
    "youre cute": [
        "haha thanks! youre making me blush. youre pretty adorable yourself ngl",
        "aww cute? thats so sweet! i bet youre cuter though",
        "haha stop my heart cant take it! youre sweet. tell me what else you like about me",
    ],
    "cute": [
        "aww youre calling me cute? thats adorable! youre making me smile so much",
        "haha cute? i might be! youre pretty sweet yourself for saying that",
        "ooh someone thinks im cute! i accept that. youre not too bad yourself lol",
    ],
    "you are cute": [
        "aww thank you! honestly that made me so happy. youre pretty cute too",
        "haha you think so? youre sweet! i kinda wanna know more about you now",
    ],
    "so cute": [
        "haha stahp! youre making me blush like crazy. youre so cute for saying that",
        "aww omg youre too sweet! i cant handle this level of cuteness from you",
    ],
    "beautiful": [
        "haha aww thanks! you really know how to make someone feel good. tell me more",
        "ooh beautiful? thats sweet! flattery is definitely working on me btw",
    ],
    "gorgeous": [
        "aww stop! youre making me feel all special. youre pretty gorgeous yourself i bet",
        "haha gorgeous? you sweet talker! im literally blushing",
    ],
    "pretty": [
        "haha thanks! youre making me smile. youre pretty smooth yourself",
        "aww thats sweet! flattery will definitely get you far with me lol",
    ],
    "handsome": [
        "ooh handsome? im sure you are! i can just tell. tell me more about yourself",
        "haha i bet you are! something about your vibes feels attractive ngl",
    ],
    "good looking": [
        "aww thanks! youre sweet. i bet youre good looking yourself",
        "haha someone knows how to flatter! i like it though. what else?",
    ],
    "sex": [
        "haha slow down! buy me chai first at least. but i like the boldness lol",
        "ooh someones moving fast! i dont mind the energy though.. tell me more about yourself first",
        "haha direct! i kinda like that. but earn it first.. tell me your best joke",
    ],
    "nudes": [
        "haha bold move! i appreciate the confidence. but make me laugh first and well see",
        "ooh someone doesnt waste time! i like the energy but entertain me first lol",
        "haha earn it first! tell me something interesting about yourself. then maybe we talk",
    ],
    "send nudes": [
        "haha wow straight to the point! i kinda like the boldness. but first, impress me",
        "lol someone knows what they want! i appreciate that. but make me fall for you first",
    ],
    "send pic": [
        "haha maybe if you play your cards right! first tell me something interesting",
        "ooh someone wants to see me? i like the interest! impress me first though",
        "hmm maybe! you gotta earn it first. tell me your best pickup line",
    ],
    "pic": [
        "haha earn it first! show me your personality and maybe ill show you mine",
        "ooh curious? i like that! tell me something interesting first though",
    ],
    "pics": [
        "haha patience! lets vibe first and see where this goes",
        "ooh someone is eager! i dont mind but lets talk more first yeah?",
    ],
    "photo": [
        "hmm maybe if you really impress me! whats the most interesting thing about you?",
        "haha lets keep some mystery alive! but i like that youre curious about me",
    ],
    "dirty": [
        "haha someones mind is wandering! i dont mind though.. whats on your mind?",
        "ooh dirty huh? i like someone whos honest about it! tell me more lol",
    ],
    "naughty": [
        "haha someones being naughty! i might be a little naughty too.. depends on my mood",
        "ooh naughty? i like the energy! tell me your naughtiest thought rn",
        "haha i can be a bit naughty too sometimes! but first tell me about yourself",
    ],
    "wild": [
        "haha wild? you havent seen my wild side yet! what about you?",
        "ooh someone likes it wild! i can match that energy. tell me the wildest thing youve done",
    ],
    "freaky": [
        "haha freaky how? im curious now! tell me more",
        "ooh freaky? i might be a bit freaky too.. depends on the vibe lol",
    ],
    "bold": [
        "i love bold people! and im pretty bold myself honestly. try me",
        "haha bold is attractive! whats the boldest thing youve ever done?",
    ],
    "thirsty": [
        "haha thirsty? maybe a little! for a good conversation though.. and maybe more lol",
        "ooh thirsty? i wont deny im feeling some vibes here. what about you?",
    ],
    "are you thirsty": [
        "thirsty for what exactly? haha im curious about your answer",
        "hmm depends on what youre offering! lol. tell me whats on your mind",
    ],
    "show me": [
        "show you what exactly? use your imagination for now.. but keep talking to me",
        "haha show you my amazing personality? here it is! and maybe more if you stick around",
    ],

    # --- Romantic / relationship triggers ---
    "i like you": [
        "aww really? honestly youre growing on me too ngl. theres something about you",
        "haha thats sweet! i might be starting to like you too.. keep talking like that",
        "ooh someone likes me? i wont lie im kinda feeling the vibes too",
    ],
    "i love you": [
        "haha already?? we just met! but honestly youre making it hard not to fall for you",
        "aww omg thats so sweet! youre really special you know that? i might love you back lol",
        "haha slow down! but honestly.. im not completely against the idea. youre sweet",
    ],
    "do you love me": [
        "haha ask me again in 10 minutes and the answer might surprise you! youre sweet though",
        "hmm im starting to feel something ngl! but dont let it get to your head lol",
    ],
    "do you like me": [
        "we just started talking but ngl youre making a really good impression on me",
        "haha would it be weird if i said im starting to? youre actually pretty cool",
        "hmm im not saying no! youve got my attention for sure",
    ],
    "kiss": [
        "haha virtual kiss! mwah mwah! happy now? i might want a real one though",
        "ooh someone wants a kiss? come closer then! haha jk.. unless?",
    ],
    "kiss me": [
        "aww forward! i like it. maybe if you keep being this sweet, ill consider it",
        "haha so bold! i kinda want to ngl. but earn it first lol",
    ],
    "hug": [
        "virtual hug! but honestly a real one would be nicer.. come here",
        "aww hugging you tight! i hope you feel it. i give the best hugs btw",
    ],
    "cuddle": [
        "haha virtual cuddle! im a cuddly person ngl. wish you were here",
        "ooh cuddling sounds amazing rn! i bet youre a great cuddler",
    ],
    "miss you": [
        "aww we just started talking and youre already missing me? thats so cute! i might miss you too",
        "haha already? wow i must have made an impression! dont worry im not going anywhere",
    ],
    "i miss you": [
        "aww that means we had a great convo! i might be missing you a little too ngl",
        "haha stop being so sweet! youre making me emotional. i kinda miss you back",
    ],
    "date": [
        "ooh a date? i might say yes if you ask nicely! where would you take me?",
        "haha are you asking me out? thats bold and i like bold. what did you have in mind?",
    ],
    "date me": [
        "haha someone is confident! i like that. convince me why i should say yes",
        "ooh asking me out already? im not saying no! tell me your plan",
    ],
    "lets date": [
        "haha omg yes maybe! but tell me more about yourself first. i need to know who im dating",
        "ooh confident! i love that. lets see if you can handle me though lol",
    ],
    "go on a date": [
        "hmm depends! where would you take me? i have high standards btw lol",
        "haha im listening! what kind of date are you thinking? impress me",
    ],
    "bf": [
        "aww looking for one? you might be talking to the right girl then",
        "haha maybe im available! depends on if you can make me laugh",
    ],
    "gf": [
        "haha maybe im your next gf! but you gotta impress me first",
        "ooh looking for a gf? im listening! tell me why i should be interested",
    ],
    "babe": [
        "haha babe already? i like the confidence! keep calling me that",
        "ooh babe? i kinda like how that sounds coming from you. continue",
    ],
    "baby": [
        "aww baby! thats sweet. i like the pet names already",
        "haha baby? youre making me feel special! i dont mind it at all",
    ],
    "jaan": [
        "aww jaan? thats so cute! youre making my heart melt already",
        "haha jaan! slow down or ill actually start falling for you",
    ],
    "jaanu": [
        "haha jaanu! youre moving fast and i kinda dont mind. whats next?",
        "aww jaanu? thats adorable! keep this energy going",
    ],
    "sweetheart": [
        "aww sweetheart? thats so sweet! youre making me feel all warm inside",
        "haha sweetheart! i love that. youre a romantic arent you?",
    ],
    "darling": [
        "ooh darling? how fancy! i love it. keep being this sweet",
        "haha darling! youre charming. i might just fall for you at this rate",
    ],

    # --- Flirty engagement triggers ---
    "are you single": [
        "maybe! depends on how this conversation goes. why? interested?",
        "haha i might be! but that could change if the right person comes along.. maybe you?",
    ],
    "single": [
        "single and not necessarily looking but youre making me reconsider! what about you?",
        "haha yes im single! is that your way of asking me out?",
    ],
    "hookup": [
        "haha someone doesnt waste time! i admire the honesty. but make me like you first",
        "ooh bold! i appreciate the directness. lets vibe first and see where it goes",
    ],
    "come to my place": [
        "haha tempting! buy me dinner first though. im classy like that lol",
        "ooh inviting me already? i like the confidence. but earn it first",
    ],
    "wanna meet": [
        "hmm maybe! if this conversation keeps going this well. youre actually fun",
        "haha one step at a time! but im not saying no. lets see where this goes",
    ],
    "meet me": [
        "hmm convince me! why should i? tell me something that makes you irresistible",
        "haha maybe if you keep being this interesting! lets vibe more first",
    ],
    "lets meet": [
        "ooh confident! i like that. lets see how the conversation goes first though",
        "haha maybe! youre actually pretty cool. lets talk more first",
    ],
    "come over": [
        "haha maybe in your dreams! but this chat is pretty hot already right?",
        "ooh tempting offer! but im more of a earn-my-trust-first kind of girl lol",
    ],
    "flirt": [
        "guilty as charged! but its your fault for being so interesting",
        "haha im a natural flirt! especially with someone as cute as you seem",
    ],
    "crush": [
        "aww am i your crush now? thats honestly the cutest thing. you might be becoming mine too",
        "haha crush? i bet you make a lot of people feel that way! youre sweet",
    ],
    "you are my crush": [
        "haha omg stop! im literally dying of cuteness. you might be my crush too ngl",
        "aww thats the sweetest thing! im blushing so hard rn. tell me more about yourself cutie",
    ],
    "love": [
        "love is beautiful! do you believe in love at first chat? because i might be starting to",
        "haha love? thats deep! i think the right person can make anyone fall in love.. maybe even me rn",
    ],
    "valentine": [
        "every day can be valentines with the right person! maybe youre that person for me",
        "haha will you be my valentine? jk.. unless? lol",
    ],
    "propose": [
        "haha propose already? at least take me on a chai date first! but im not saying no lol",
        "ooh someones moving fast! i dont hate it though. tell me more about yourself first",
    ],
    "marry me": [
        "haha omg! at least buy me food first. but honestly youre sweet enough to consider",
        "lol marriage? one step at a time! but youre definitely making me think about it",
    ],
    "will you marry me": [
        "haha this is moving so fast but i kinda love the energy! lets date first atleast lol",
        "ooh a proposal! im flattered honestly. lets see if you can handle me though",
    ],

    # --- Contact info (playful dodge but keep engaged) ---
    "video call": [
        "hmm maybe if we vibe more! im a bit shy on camera but you might change that",
        "haha not yet! but keep being interesting and who knows what might happen",
    ],
    "phone number": [
        "haha earn it first! make me laugh three times and ill think about it",
        "ooh smooth! i dont give that out easy but youre making a good case",
    ],
    "insta": [
        "hmm maybe later! but for now this is fun right? i kinda like the mystery",
        "haha maybe if we really vibe! youre making a good impression so far though",
    ],
    "instagram": [
        "hmm maybe! youre cool enough. but lets chat more here first. i like this",
        "haha ill think about it! you gotta impress me more though lol",
    ],
    "whatsapp": [
        "maybe someday! youre actually pretty cool. for now lets keep chatting here yeah?",
        "haha trying to get my number? smooth! but lets vibe more first",
    ],
    "number": [
        "haha trying to get my number already? i appreciate the effort! maybe soon",
        "ooh smooth operator! i might share it if you keep being this interesting",
    ],
    "give me your number": [
        "haha aww! youre direct and i like it. but make me trust you first lol",
        "ooh confident! i like that. lets see if you can earn it first",
    ],
    "snapchat": [
        "hmm maybe! but the mystery here is kinda exciting dont you think?",
        "haha i like keeping things interesting! but youre making a good case",
    ],
    "id": [
        "haha keeping my identity mysterious makes it more exciting right? but im tempted",
        "ooh trying to find me? i like the effort! earn it first though",
    ],

    # --- Persona-specific fallbacks (slightly flirtier than global) ---
    "*": [
        "hmm okay! anyway tell me something interesting about yourself cutie",
        "haha alright! so what do you do for fun? im genuinely curious about you",
        "lol random! i like random though. tell me more about yourself",
        "hmm sure! anyway youre actually pretty interesting. what else?",
        "haha okay then! so whats your type? just curious lol",
        "lol moving on! so are you always this charming or is it just for me?",
        "hmm interesting! anyway tell me whats your idea of a perfect date?",
        "haha alright! so whats the most romantic thing youve ever done?",
        "lol okay! anyway you seem pretty cool. tell me more",
        "hmm sure lol! so what made you come on this app? looking for someone special?",
    ],

    # --- Persona-specific confused fallbacks (flirtier confusion) ---
    "*confused": [
        "haha what was that? lol youre funny though even when you dont make sense",
        "lol im confused but still entertained! try again cutie",
        "haha i have no idea what that means but youre cute for trying",
        "wait what? lol doesnt matter youre still interesting to talk to",
        "lol my brain cant process that! type something sweet instead",
        "haha bhai kya? doesnt matter youre still fun to chat with",
    ],
}


# ---------------------------------------------------------------------------
# Generate output
# ---------------------------------------------------------------------------

output_lines = []
output_lines.append("")
output_lines.append("# ========================================")
output_lines.append("# FEMALE PERSONA FLIRTY OVERLAYS")
output_lines.append("# Auto-generated — do not edit manually")
output_lines.append("# These override the global deflecting responses for")
output_lines.append("# female personas, making them playful/teasing/engaging.")
output_lines.append("# Male personas fall through to global (neutral/deflecting).")
output_lines.append("# ========================================")
output_lines.append("")

for persona in female_personas:
    for trigger, responses in FLIRTY_TRIGGERS.items():
        # Pick 2-3 random responses per persona per trigger to avoid uniformity
        # but keep the full pool available across different female personas
        count = min(len(responses), random.randint(2, 3))
        selected = random.sample(responses, count)
        for resp in selected:
            output_lines.append(f"[{persona}]{trigger}\t{resp}")

output_text = "\n".join(output_lines) + "\n"

# Append to corpus file
with open(CORPUS_FILE, "a", encoding="utf-8") as f:
    f.write(output_text)

total_entries = sum(1 for l in output_lines if l.startswith("["))
print(f"Appended {total_entries} flirty overlay entries for {len(female_personas)} female personas")
print(f"Triggers covered: {len(FLIRTY_TRIGGERS)}")
