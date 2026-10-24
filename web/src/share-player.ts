import type Hls from 'hls.js'
const audio=document.getElementById('share-audio') as HTMLAudioElement|null
if(audio) {
 const player=audio
 const buttons=[...document.querySelectorAll<HTMLButtonElement>('#share-tracks button')]
 const status=document.getElementById('share-status')!
 const speed=document.getElementById('share-speed') as HTMLSelectElement
 const key='pab-shared:'+document.body.dataset.share
 let index=0,pending=0,restoring=false,serial=0,converted=false,wantedPlay=false,hls:Hls|undefined
 const save=()=>{if(restoring)return;try{localStorage.setItem(key,JSON.stringify({id:buttons[index]?.dataset.id,time:player.currentTime,rate:player.playbackRate}))}catch{status.textContent='Browser storage is unavailable; your place will not survive closing this tab.'}}
 const play=()=>player.play().catch(()=>{if(!player.error)status.textContent='Press play to continue.'})
 const load=(i:number,at:number,autoplay:boolean)=>{
  serial++;hls?.destroy();hls=undefined;index=i;pending=Number.isFinite(at)?Math.max(0,at):0;restoring=true;converted=false;wantedPlay=autoplay
  buttons.forEach((b,j)=>b.classList.toggle('selected',j===i));document.getElementById('track-title')!.textContent=buttons[i].textContent
  player.src=buttons[i].dataset.src!;player.load();if(autoplay)void play()
 }
 const convert=async()=>{
  if(converted){status.textContent='The audio could not be played. The link may have expired or been revoked.';return}
  converted=true;const generation=serial;status.textContent='Preparing audio for this browser…'
  if(!restoring)pending=player.currentTime;restoring=true
  const url=buttons[index].dataset.src!.replace('/file/','/hls/')+'/index.m3u8'
  try {
   const {default:HLS}=await import('hls.js');if(serial!==generation)return
   if(player.canPlayType('application/vnd.apple.mpegurl')){player.src=url;player.load();if(wantedPlay)void play()}
   else if(HLS.isSupported()){
    hls=new HLS({enableWorker:false});hls.loadSource(url);hls.attachMedia(player)
    hls.on(HLS.Events.MANIFEST_PARSED,()=>{if(serial===generation && wantedPlay)void play()})
    hls.on(HLS.Events.ERROR,(_event,data)=>{if(data.fatal)status.textContent='Audio conversion failed, or this link is no longer available.'})
   } else status.textContent='This browser cannot play the audio. Try an RSS client or another browser.'
  } catch {status.textContent='Could not load the player. Reload and try again.'}
 }
 player.addEventListener('loadedmetadata',()=>{player.currentTime=Math.min(pending,Math.max(0,player.duration-0.1));player.playbackRate=Number(speed.value);restoring=false;status.textContent='Your place is saved in this browser.'})
 player.addEventListener('play',()=>{wantedPlay=true})
 player.addEventListener('timeupdate',save)
 player.addEventListener('pause',save)
 player.addEventListener('ended',()=>{if(index+1<buttons.length)load(index+1,0,true)})
 player.addEventListener('error',()=>void convert())
 speed.addEventListener('change',()=>{player.playbackRate=Number(speed.value);save()})
 buttons.forEach((b,i)=>b.addEventListener('click',()=>{save();load(i,0,true)}))
 let saved:{id?:string;time?:number;rate?:number}|undefined
 try{saved=JSON.parse(localStorage.getItem(key)||'null')}catch{}
 let i=buttons.findIndex(b=>b.dataset.id===saved?.id);if(i<0)i=0
 if(saved?.rate && [0.75,1,1.25,1.5,1.75,2].includes(saved.rate))speed.value=String(saved.rate)
 if(buttons.length)load(i,saved?.time||0,false)
 window.addEventListener('pagehide',save)
}
