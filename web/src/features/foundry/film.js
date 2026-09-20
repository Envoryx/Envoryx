/*
 * ENVORYX / ASCII ODYSSEY
 * Standalone, deterministic 38-second cinematic. No network requests or assets.
 * All world geometry, the explorer and the brand reveal use ASCII glyphs.
 * A cached glyph atlas avoids repeated font rasterization in the animation loop.
 * Scene timing is measured in active seconds; hidden tabs never advance the film.
 */

/** Mounts the film into `root` (the markup from FoundryPage) and returns a function that stops it again. */
export function mountFilm(root) {
    const $ = id => root.querySelector('#'+id);
    const canvas = $('world');
    const ctx = canvas.getContext('2d', { alpha: false });
    const movie = $('movie');
    const ui = Object.fromEntries(['chapterTag','sceneName','coordinates','status','terminalLine','play','clock','chapters','announcement','reduced'].map(id => [id, $(id)]));
    const DURATION = 38;
    const CHAPTERS = [
        {start:0,end:7,code:'THE FIRST COMMIT',short:'Source',title:'Step inside the build.',command:'git checkout possibility',location:'INPUT / SOURCE MATERIAL'},
        {start:7,end:17,code:'ASSEMBLY LINE',short:'Assembly',title:'Two lines. One endless possibility.',command:'docker compose up --build',location:'TWIN ASSEMBLY / BAY 04'},
        {start:17,end:26,code:'QUALITY CONTROL',short:'Testing',title:'Built to keep moving.',command:'test && connect && create',location:'VALIDATION / ALL SYSTEMS GREEN'},
        {start:26,end:28,code:'THE BUILD ENGINE',short:'Build',title:'Turn possibility into something real.',command:'envoryx build --deeper',location:'CORE FOUNDRY / ONLINE'},
        {start:28,end:36,code:'BUILD DEEPER',short:'Envoryx',title:'Made for the things you will make.',command:'build complete. next idea?',location:'ENVORYX / READY TO CREATE'},
        {start:36,end:38,code:'ANOTHER BEGINNING',short:'Repeat',title:'There is always another idea.',command:'queue.push(nextIdea)',location:'FACTORY / CONTINUOUS OPERATION'}
    ];
    const COLORS = ['#294339','#537c68','#8aab9d','#c6d8d0','#f0f1f2','#27f2bf','#20a77e'];
    const CODE = ['const world = create();','await stack.ready();','> build --watch','fn explore(depth) {','  return more(depth + 1);','} // keep going','0101 0010 1101','git commit -m "begin"','export { possibility };','[ok] all systems ready','pipe(input, transform)','docker compose up -d'];
    const FONT = {
        E:['11111','10000','11110','10000','11111'], N:['10001','11001','10101','10011','10001'],
        V:['10001','10001','10001','01010','00100'], O:['01110','10001','10001','10001','01110'],
        R:['11110','10001','11110','10100','10010'], Y:['10001','01010','00100','00100','00100'],
        X:['10001','01010','00100','01010','10001']
    };
    const motionQuery = matchMedia('(prefers-reduced-motion: reduce)');
    let reduced = motionQuery.matches;
    let playing = !reduced;
    let elapsed = reduced ? 33 : 2;
    let raf = 0, previous = null, lastPaint = -Infinity, currentChapter = -1;
    let width = 1, height = 1, cols = 160, rows = 60, cw = 8, ch = 13, dpr = 1;
    let atlas, sourceW, sourceH;
    const clamp = (v, a=0, b=1) => Math.min(b, Math.max(a, v));
    const smooth = v => { v=clamp(v); return v*v*(3-2*v); };
    const mix = (a,b,t) => a+(b-a)*t;
    const mod = (a,b) => ((a%b)+b)%b;
    const hash = n => { const x=Math.sin(n*127.1+311.7)*43758.5453; return x-Math.floor(x); };

    /* Render text from one compact, high-DPI atlas, with smooth sub-cell movement. */
    function buildAtlas() {
        sourceW = Math.ceil(cw*dpr);
        sourceH = Math.ceil(ch*dpr);
        atlas = document.createElement('canvas');
        atlas.width = sourceW*95; atlas.height = sourceH*COLORS.length;
        const a = atlas.getContext('2d');
        a.font = `${Math.max(7, ch*.90)*dpr}px ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", "DejaVu Sans Mono", monospace`;
        a.textBaseline = 'middle'; a.textAlign = 'center';
        for (let color=0;color<COLORS.length;color++) {
            a.fillStyle=COLORS[color];
            for(let c=32;c<127;c++) a.fillText(String.fromCharCode(c),(c-32)*sourceW+sourceW/2,color*sourceH+sourceH/2);
        }
    }
    function glyph(char,x,y,color=3,scale=1) {
        if(char===' ' || x < -scale || y < -scale || x > cols || y > rows) return;
        const index=char.charCodeAt(0)-32;
        if(index<0 || index>94) return;
        ctx.drawImage(atlas,index*sourceW,color*sourceH,sourceW,sourceH,x*cw,y*ch,cw*scale,ch*scale);
    }
    function text(str,x,y,color=3,scale=1) { for(let i=0;i<str.length;i++) glyph(str[i],x+i*scale,y,color,scale); }
    function art(lines,x,y,color=3,scale=1) { lines.forEach((line,i)=>text(line,x,y+i*scale,color,scale)); }
    function center(str,y,color=3,scale=1) { text(str,(cols-str.length*scale)/2,y,color,scale); }
    function line(x,y,length,char='-',color=2) { text(char.repeat(Math.max(0,Math.floor(length))),x,y,color); }
    function box(x,y,w,h,color=2) {
        line(x+1,y,w-2,'-',color); line(x+1,y+h-1,w-2,'-',color);
        for(let j=1;j<h-1;j++){glyph('|',x,y+j,color);glyph('|',x+w-1,y+j,color);}
        for(const p of [[x,y],[x+w-1,y],[x,y+h-1],[x+w-1,y+h-1]]) glyph('+',...p,color);
    }
    function panel(x,y,w,h,color=2) {
        ctx.fillStyle='#080e0d';ctx.fillRect(x*cw,y*ch,w*cw,h*ch);box(x,y,w,h,color);
    }

    /* Real perspective projection of a small 3D scene, rendered with ASCII glyphs. */
    function production(t) {
        const phase=mod(t,7),cycle=Math.floor(t/7),travel=smooth((phase-4)/3);
        return {phase,cycle,travel,working:phase>=1.2&&phase<3.2};
    }
    function project3(p,t=0) {
        // A gentle dolly and lateral orbit keep the production floor spatially alive.
        const sway=Math.sin(t*.17)*.28,zoom=1+.10*Math.sin(t*.14);
        const depth=Math.max(.1,p[2]+5.2-.35*Math.sin(t*.12));
        const k=5.2/depth*zoom;
        return {x:cols*.5+(p[0]-sway)*k*cols*.145,y:rows*.40+(3.45-p[1])*k*rows*.14,k};
    }
    function edge3(a,b,t,color=2,char=':') {
        const p=project3(a,t),q=project3(b,t);
        const n=Math.min(150,Math.max(1,Math.ceil(Math.hypot(q.x-p.x,(q.y-p.y)*1.6))));
        for(let i=0;i<=n;i++)glyph(char,mix(p.x,q.x,i/n),mix(p.y,q.y,i/n),color,.9);
    }
    function face3(points,t,fill='#0b1411') {
        ctx.beginPath();points.forEach((v,i)=>{const p=project3(v,t);if(i)ctx.lineTo(p.x*cw,p.y*ch);else ctx.moveTo(p.x*cw,p.y*ch);});
        ctx.closePath();ctx.fillStyle=fill;ctx.fill();
    }
    function label3(str,p,t,color=3,scale=.9) {
        const q=project3(p,t),size=clamp(q.k*scale,.42,1.25);
        text(str,q.x-str.length*size/2,q.y,color,size);
    }
    function light3(p,t,radius,strength=.16) {
        const q=project3(p,t),r=radius*q.k*cw;
        const gradient=ctx.createRadialGradient(q.x*cw,q.y*ch,0,q.x*cw,q.y*ch,r);
        gradient.addColorStop(0,`rgba(39,242,191,${strength})`);
        gradient.addColorStop(.3,`rgba(39,242,191,${strength*.3})`);
        gradient.addColorStop(1,'rgba(39,242,191,0)');
        ctx.fillStyle=gradient;ctx.fillRect(q.x*cw-r,q.y*ch-r,r*2,r*2);
    }
    function atmosphere(t) {
        // Lighting is restrained; all hard surfaces and beams remain ASCII geometry.
        light3([0,2.6,18],t,55,.22);
        for(let side of [-1,1])for(let z of [2,10,18]){
            light3([side*2.4,1.05,z],t,20,.13);
            for(let j=0;j<12;j++){
                const phase=mod(t*1.8+j*.53+z,7),p=project3([side*(1.3+hash(j+z)),phase*.45,z+hash(j)*3],t);
                glyph(j%5?'.':'+',p.x,p.y,j%5?1:6,.6);
            }
        }
    }
    function hall(t) {
        // A central aisle remains empty so both production lines read clearly.
        for(let x=-5;x<=5;x++)edge3([x,0,0],[x,0,26],t,x===-1||x===1?2:0,'.');
        for(let z=0;z<=26;z+=2)edge3([-5,0,z],[5,0,z],t,0,'.');
        for(let side of [-1,1]){
            edge3([side*4.7,0,0],[side*4.7,0,26],t,1);
            edge3([side*4.7,5.4,0],[side*4.7,5.4,26],t,2,'=');
            for(let z=1;z<27;z+=4){
                edge3([side*4.7,0,z],[side*4.7,5.4,z],t,1,'|');
                edge3([side*4.7,5.4,z],[side*2.8,5.4,z],t,3,'=');
                edge3([side*4.7,4.4,z],[side*3.8,5.4,z],t,1,'/');
            }
        }
        for(let z=2;z<27;z+=4){
            edge3([-4.7,5.6,z],[4.7,5.6,z],t,1,'-');
            edge3([-.65,5.58,z],[.65,5.58,z],t,4,'=');
        }
        // Suspended status boards and wall racks establish industrial scale.
        for(let side of [-1,1])for(let z of [5,13,21]){
            const x=side*4.5;
            for(let j=0;j<6;j++){
                edge3([x,1.2+j*.36,z],[x,1.2+j*.36,z+2.5],t,2,'=');
                const q=project3([x,1.3+j*.36,z+.15],t);glyph(Math.floor(t*3+j)%4?'o':'*',q.x,q.y,5,.65);
            }
            label3(side<0?'COMPOSE / ONLINE':'BUILD / VERIFIED',[side*2.5,4.95,z],t,5,.95);
        }
        // The destination machine is mounted at the end of the aisle.
        const z=18;
        face3([[-1.7,.1,z],[1.7,.1,z],[1.7,4.8,z],[-1.7,4.8,z]],t,'#0d1a16');
        for(const [a,b] of [[[-1.7,.1,z],[-1.7,4.8,z]],[[1.7,.1,z],[1.7,4.8,z]],[[-1.7,4.8,z],[1.7,4.8,z]],[[-1.7,.1,z],[1.7,.1,z]]])edge3(a,b,t,3,'#');
        label3('ENVORYX', [0,4.5,z],t,5,1.6);
        for(let r=0;r<5;r++)for(let i=0;i<64;i++){
            const a=i/64*Math.PI*2+t*(r%2?.7:-.6),p=project3([Math.cos(a)*(.55+r*.24),2.6+Math.sin(a)*(.55+r*.24),z-.1],t);
            glyph(i%6?':':'#',p.x,p.y,i%6?2:5,.65);
        }
        label3('[ BUILD CORE ]',[0,.8,z],t,3,1.2);
        const energy=smooth((t-24)/10);
        light3([0,2.6,z-.5],t,30,.12+energy*.3);
        for(let i=0;i<32;i++){
            const a=i/32*Math.PI*2+t*.15,r=1.5+energy*.4;
            const q=project3([Math.cos(a)*r,2.6+Math.sin(a)*r,z-.3],t);
            glyph(i%4?':':'+',q.x,q.y,i%4?2:5,.75);
        }
        if(t>26)for(let i=0;i<24;i++){
            const zFlow=mod(i*1.2-t*3,25);
            const q=project3([Math.sin(i*3)*.7,.1,zFlow],t);glyph('>',q.x,q.y,5,.7);
        }
    }
    function conveyor3(side,t) {
        const state=production(t),x=side*2.55,lo=x-.85,hi=x+.85;
        face3([[lo,1,-3.9],[hi,1,-3.9],[hi,1,25],[lo,1,25]],t,'#0c1813');
        for(const xx of [lo,hi]){
            edge3([xx,1,-3.9],[xx,1,25],t,3,'=');
            edge3([xx,.65,-3.9],[xx,.65,25],t,2,'-');
        }
        for(let z=-3.9;z<25;z+=.65){
            edge3([lo,1,z],[hi,1,z],t,1,'-');
            const phase=state.travel*4;
            const q=project3([x,1.04,mod(z+3.9-phase,28.9)-3.9],t);
            glyph('v',q.x,q.y,6,Math.max(.5,q.k));
        }
        // Continuous illuminated safety rails frame the dark central aisle.
        const inner=x-side*.9;
        edge3([inner,1.06,-3.9],[inner,1.06,25],t,6,'=');
        for(let j=0;j<16;j++){
            const zPulse=mod(j*1.9-state.travel*4,28.9)-3.9;
            const q=project3([inner,1.08,zPulse],t);glyph('#',q.x,q.y,5,Math.max(.45,q.k*.7));
        }
        for(let z=1;z<25;z+=4){
            for(const xx of [lo,hi]){
                edge3([xx,0,z],[xx,1,z],t,2,'|');
                edge3([xx,.05,z-.25],[xx,.05,z+.25],t,2,'=');
            }
            edge3([lo,.2,z],[hi,.7,z],t,1,'/');
        }
        label3(side<0?'LINE A / COMPOSE':'LINE B / VERIFY',[x,.35,.3],t,5,.8);
    }
// Cull cargo only after its entire projected box has left the viewport.
    function cargoVisible(side,z,t) {
        const points=[];
        for(const x of [side*2.55-.72,side*2.55+.72])
            for(const y of [1.06,2.35])
                for(const depth of [z-.63,z+.63])points.push(project3([x,y,depth],t));
        return !(points.every(p=>p.x<-2)||points.every(p=>p.x>cols+2)||
            points.every(p=>p.y<-2)||points.every(p=>p.y>rows+2));
    }
    function cargo3(side,z,t,id) {
        const x=side*2.55,w=.72,d=.63,b=1.06,h=2.35;
        const v=[[x-w,b,z-d],[x+w,b,z-d],[x+w,h,z-d],[x-w,h,z-d],
            [x-w,b,z+d],[x+w,b,z+d],[x+w,h,z+d],[x-w,h,z+d]];
        face3([v[3],v[2],v[6],v[7]],t,'#12281f');
        face3(side<0?[v[1],v[5],v[6],v[2]]:[v[0],v[4],v[7],v[3]],t,'#102019');
        face3(v.slice(0,4),t,'#101b17');
        for(const [a,b] of [[0,1],[1,2],[2,3],[3,0],[2,6],[3,7],[6,7],[0,4],[1,5],[4,7],[5,6]])edge3(v[a],v[b],t,a<4&&b<4?3:2,a===b?'#':':');
        const label=['NODE.JS','POSTGRES','PHP','REDIS','NGINX','CADDY'][mod(id,6)];
        // Corner locks, corrugated faces, vents, and lid seams give each box weight.
        for(let j=0;j<7;j++){
            const xx=x-w+.1+j*.21;
            edge3([xx,1.11,z-d-.015],[xx,2.3,z-d-.015],t,1,'|');
        }
        for(const xx of [x-w+.04,x+w-.04])for(const yy of [b+.06,h-.06]){
            const q=project3([xx,yy,z-d-.02],t);glyph('#',q.x,q.y,4,Math.max(.5,q.k*.7));
        }
        label3('DOCKER',[x,2.17,z-d-.025],t,5,1.05);
        label3(label,[x,1.78,z-d-.01],t,4,1.1);
        const state=production(t);
        label3(state.phase<1.2?'QUEUED':state.phase<3.2?'BUILD':'RUNNING',[x,1.36,z-d-.01],t,state.phase<1.2?2:5,.83);
        for(let j=0;j<5;j++)edge3([x-w+.15+j*.28,h,z-d],[x-w+.15+j*.28,h,z+d],t,2,'-');
    }
    function robot3(side,z,t) {
        const state=production(t),p=state.phase;
        const down=smooth(p/1.2)*(1-smooth((p-3.2)/.8));
        const base=[side*4.1,4.4,z],elbow=[side*3.5,4.15-down*.45,z];
        const tool=[side*2.55,mix(3.6,2.5,down),z];
        edge3([side*4.1,5.4,z],base,t,3,'#');
        edge3(base,elbow,t,4,'#');edge3(elbow,tool,t,3,'=');
        edge3([base[0]+side*.12,base[1],z], [elbow[0]+side*.12,elbow[1],z],t,2,'#');
        edge3([elbow[0],elbow[1]+.14,z],[tool[0],tool[1]+.14,z],t,4,'-');
        edge3([side*4.1,4.5,z-.22],[side*4.1,4.5,z+.22],t,3,'=');
        edge3([base[0],base[1]-.13,z], [elbow[0],elbow[1]-.13,z],t,2,':');
        for(const joint of [base,elbow,tool])label3('(o)',joint,t,5,.8);
        const gap=mix(.65,.38,down);
        edge3([tool[0]-gap,tool[1],z],[tool[0]+gap,tool[1],z],t,4,'-');
        for(const sign of [-1,1])edge3([tool[0]+gap*sign,tool[1],z],[tool[0]+gap*sign,tool[1]-.2,z],t,4,'|');
        if(state.working){
            light3([tool[0],2.35,z],t,11,.30);
            for(let i=0;i<22;i++){
                const age=mod(t*5+i*.22,1),q=project3([tool[0]+Math.sin(i*7)*age*.5,2.35+age*.95,z+Math.cos(i*5)*age*.4],t);
                glyph(i%3?'.':'+',q.x,q.y,i%3?5:4,Math.max(.6,q.k));
            }
        }
        label3(state.working?'[ BUILD ]':p>=4?'[ FEED ]':'[ CLAMP ]',[side*4,4.9,z],t,5,.7);
    }
    function world(t) {
        atmosphere(t);
        hall(t);
        conveyor3(-1,t);conveyor3(1,t);
        const state=production(t),objects=[];
        // Sort by depth so nearby cargo occludes distant arms and containers correctly.
        for(let side of [-1,1]){
            for(let i=-1;i<7;i++){
                const z=2+i*4-state.travel*4;
                if(cargoVisible(side,z,t))objects.push({z,draw:()=>cargo3(side,z,t,i+state.cycle+(side>0?3:0))});
            }
            for(let i=0;i<6;i++){
                const z=2+i*4;objects.push({z:z-.02,draw:()=>robot3(side,z,t)});
            }
        }
        objects.sort((a,b)=>b.z-a.z).forEach(o=>o.draw());
        const caption=state.phase<4?'ASSEMBLY IN PROGRESS':'TRANSFERRING TO NEXT STATION';
        center(caption,rows-6,5,.8);
        center('TWIN LINE / CONTAINER NATIVE',rows-4,2,.65);
    }

    /* Rasterize the supplied logo's visual grammar into ASCII strokes. */
    function logoGlyphs() {
        const points=[];
        for(let y=0;y<17;y++)for(let x=0;x<45;x++){
            const left=Math.abs(x-(Math.abs(y-8)*1.15+1))<2;
            const right=Math.abs(x-(43-Math.abs(y-8)*1.15))<2;
            const top=y<=2&&x>=15&&x<=29;
            const mid=y>=7&&y<=9&&x>=15&&x<=23;
            const bottom=y>=14&&x>=15&&x<=29;
            if(left||right||top||mid||bottom)points.push({x,y,color:left||right?5:4,char:left?'/':right?'\\':bottom?'=':'#'});
        }
        return points;
    }
    const logo=logoGlyphs();
    function wordmark(y,amount) {
        const word='ENVORYX',unit=Math.min(1.32,(cols-10)/83), total=83*unit;
        let offset=(cols-total)/2;
        for(let letter=0;letter<word.length;letter++){
            const pixels=FONT[word[letter]];
            pixels.forEach((row,j)=>{for(let k=0;k<5;k++)if(row[k]==='1'){
                const threshold=hash(letter*99+j*5+k);
                if(threshold<amount)text('##',offset+k*unit*2,y+j*unit,letter<3?5:4,unit);
                else if(threshold<amount+.12)text('::',offset+k*unit*2,y+j*unit,2,unit);
            }});
            offset+=12*unit;
        }
    }
    function finale(t) {
        const local=t-28,assembly=smooth(local/3.1);
        const scale=Math.min(1.6,(cols-18)/48,(rows-12)/32);
        const lx=(cols-45*scale)/2,ly=(rows-32*scale)/2;
        const cx=cols/2,cy=ly+8*scale;
        // A restrained glow under thousands of ASCII strokes gives the core depth.
        const glow=ctx.createRadialGradient(cx*cw,cy*ch,0,cx*cw,cy*ch,width*.42);
        glow.addColorStop(0,`rgba(39,242,191,${.07+.06*assembly})`);
        glow.addColorStop(1,'rgba(39,242,191,0)');ctx.fillStyle=glow;ctx.fillRect(0,0,width,height);
        // Orbiting data filaments contract into the symbol, then settle behind it.
        for(let ring=0;ring<3;ring++)for(let i=0;i<90;i++){
            const a=i/90*Math.PI*2+local*(ring%2?-.25:.2);
            const r=mix(cols*.58,26*scale,assembly)*(1+ring*.12);
            const x=cx+Math.cos(a)*r,y=cy+Math.sin(a)*r*.32;
            ctx.globalAlpha=(1-assembly*.65)*(.35+ring*.15);
            glyph(i%10?'.':'+',x,y,i%10?2:5,.8);
        }
        ctx.globalAlpha=1;
        for(let i=0;i<logo.length;i++){
            const p=logo[i],delay=hash(i)*.3,progress=smooth((assembly-delay)/(1-delay));
            const a=hash(i+55)*Math.PI*2+local*(1-progress)*1.2;
            const radius=(1-progress)*(cols*.65+hash(i+77)*15);
            const x=lx+p.x*scale+Math.cos(a)*radius,y=ly+p.y*scale+Math.sin(a)*radius*.45;
            ctx.globalAlpha=.25+.75*progress;
            const sweep=mod(local*15,65)-10;
            const highlight=progress>.99&&Math.abs(p.x-sweep)<1.4;
            glyph(progress>.96?p.char:'.:+*'[i%4],x,y,highlight?4:p.color,scale);
        }
        ctx.globalAlpha=1;
        // Two slow confirmation pulses travel outward after the assembly locks.
        for(const start of [3.1,6.0]){
            const age=(local-start)/2.3;
            if(age>0&&age<1){
                ctx.globalAlpha=(1-age)*.5;
                for(let i=0;i<130;i++){
                    const a=i/130*Math.PI*2,r=(23+age*35)*scale;
                    glyph(i%6?'.':'+',cx+Math.cos(a)*r,cy+Math.sin(a)*r*.48,5,.8);
                }
            }
        }
        ctx.globalAlpha=1;
        const wy=ly+20*scale;
        wordmark(wy,smooth((local-2.1)/1.8));
        const tagline='B U I L D   _   D E E P E R',ts=Math.min(1.3,scale),tx=(cols-tagline.length*ts)/2;
        if(local>3.5){ctx.globalAlpha=smooth((local-3.5)/1.1);text(tagline,tx,wy+8*scale,4,ts);glyph('_',tx+tagline.indexOf('_')*ts,wy+8*scale,5,ts);}
        ctx.globalAlpha=1;
        if(local>5){ctx.globalAlpha=smooth((local-5)/1.1)*.65;center('YOUR NEXT IDEA STARTS HERE',wy+11*scale,6,.7);ctx.globalAlpha=1;}
    }
    function updateInterface(t,force=false) {
        const index=CHAPTERS.findIndex(ch=>t>=ch.start&&t<ch.end);
        if(index!==currentChapter || force){
            currentChapter=Math.max(0,index);const scene=CHAPTERS[currentChapter];
            ui.chapterTag.textContent=`0${currentChapter+1} / ${scene.code}`;
            ui.sceneName.textContent=scene.title;ui.coordinates.textContent=scene.location;
            ui.terminalLine.textContent='> '+scene.command+' _';
            [...ui.chapters.children].forEach((button,i)=>{if(i===currentChapter)button.setAttribute('aria-current','step');else button.removeAttribute('aria-current');});
        }
        ui.clock.textContent='00:'+String(Math.floor(t)).padStart(2,'0');
        [...ui.chapters.children].forEach((button,i)=>button.querySelector('.fill').style.transform=`scaleX(${clamp((t-CHAPTERS[i].start)/(CHAPTERS[i].end-CHAPTERS[i].start))})`);
    }
    function draw(t) {
        ctx.setTransform(dpr,0,0,dpr,0,0);ctx.globalAlpha=1;ctx.fillStyle='#080c0d';ctx.fillRect(0,0,width,height);
        if(t<28){world(t);const transition=smooth((t-27.2)/.8);if(transition>0){ctx.fillStyle=`rgba(8,12,13,${transition})`;ctx.fillRect(0,0,width,height);}}
        if(t>=28){ctx.globalAlpha=1;finale(t);}
        // A quiet vignette frames the text, without glowing neon or flashing cuts.
        const vignette=ctx.createRadialGradient(width*.5,height*.48,width*.2,width*.5,height*.5,width*.75);
        vignette.addColorStop(0,'#080c0d00');vignette.addColorStop(1,'#080c0daa');ctx.fillStyle=vignette;ctx.fillRect(0,0,width,height);
        // Slow scanline and rare local glitches remain restrained; disabled at rest.
        if(playing&&!reduced){
            const scan=mod(t*3.4,rows);line(1,scan,cols-2,'_',0);
            if(t>8&&t<34&&mod(t,8)>.1&&mod(t,8)<.21)text(':: LOST PACKET / RETRY ::',cols*.67,rows*.3,2);
        }
        // Fade down, reset the world invisibly, then fade up into a fresh terminal.
        const fade=t>37?smooth(t-37):t<1?1-smooth(t):0;
        if(fade>0){ctx.fillStyle=`rgba(8,12,13,${fade})`;ctx.fillRect(0,0,width,height);}
        updateInterface(t);
    }
    function resize() {
        const bounds=movie.getBoundingClientRect();width=Math.max(1,bounds.width-2);height=Math.max(1,bounds.height-2);
        dpr=Math.min(devicePixelRatio||1,2);canvas.width=Math.round(width*dpr);canvas.height=Math.round(height*dpr);
        cols=clamp(Math.floor(width/8.4),72,220);cw=width/cols;
        rows=clamp(Math.round(height/(cw*1.65)),43,80);ch=height/rows;
        buildAtlas();draw(elapsed);
    }
    function tick(now) {
        raf=0;if(!playing||document.hidden)return;
        if(previous!==null)elapsed=mod(elapsed+Math.min((now-previous)/1000,.1),DURATION);
        previous=now;
        // Cap the dense perspective scene at 24 Hz to limit battery and CPU usage.
        if(now-lastPaint>=1000/24-1){draw(elapsed);lastPaint=now;}
        raf=requestAnimationFrame(tick);
    }
    function schedule() {if(playing&&!document.hidden&&!raf){previous=null;raf=requestAnimationFrame(tick);}}
    function syncPlayback() {
        ui.play.textContent=playing?'II PAUSE':'> PLAY';
        ui.play.setAttribute('aria-label',playing?'Pause animation':'Play animation');
        ui.status.textContent=playing?'SYSTEM ONLINE':reduced?'REDUCED MOTION / STILL FRAME':'EXPLORATION PAUSED';
        ui.reduced.classList.toggle('visible',reduced);
        if(playing)schedule();else{cancelAnimationFrame(raf);raf=0;previous=null;draw(elapsed);}
    }
    function toggle(){playing=!playing;syncPlayback();}
    function restart(){elapsed=reduced?2:0;previous=null;draw(elapsed);if(!reduced)playing=true;syncPlayback();}
    function seek(index){elapsed=CHAPTERS[index].start+(index===0?2:0.05);previous=null;draw(elapsed);ui.announcement.textContent=`Chapter ${index+1}: ${CHAPTERS[index].short}`;}
    async function fullscreen(){
        try{if(document.fullscreenElement)await document.exitFullscreen();else if(movie.requestFullscreen)await movie.requestFullscreen();}
        catch{ui.announcement.textContent='Fullscreen is not available in this browser.';}
    }
    CHAPTERS.forEach((scene,i)=>{
        const button=document.createElement('button');button.className='chapter';button.setAttribute('aria-label',`Chapter ${i+1}: ${scene.short}`);
        button.innerHTML=`<span class="track"><span class="fill"></span></span><span class="label">0${i+1} ${scene.short}</span>`;
        button.addEventListener('click',()=>seek(i));ui.chapters.appendChild(button);
    });
    $('play').addEventListener('click',toggle);
    $('replay').addEventListener('click',restart);
    $('fullscreen').addEventListener('click',fullscreen);
    // Shortcuts are page-wide while the film is mounted, but never steal keys from form fields.
    const onKey=event=>{
        if(event.altKey||event.ctrlKey||event.metaKey||event.repeat)return;
        const el=document.activeElement;
        if(el&&(['INPUT','SELECT','TEXTAREA'].includes(el.tagName)||el.isContentEditable))return;
        if(event.code==='Space'&&el?.tagName!=='BUTTON'){event.preventDefault();toggle();}
        if(event.key.toLowerCase()==='r')restart();if(event.key.toLowerCase()==='f')fullscreen();
    };
    const onVisibility=()=>{previous=null;if(document.hidden){cancelAnimationFrame(raf);raf=0;}else schedule();};
    const onMotion=event=>{reduced=event.matches;if(reduced){playing=false;elapsed=33;}syncPlayback();};
    document.addEventListener('keydown',onKey);
    document.addEventListener('visibilitychange',onVisibility);
    motionQuery.addEventListener('change',onMotion);
    const observer=new ResizeObserver(resize);observer.observe(movie);
    resize();syncPlayback();
    return()=>{
        cancelAnimationFrame(raf);raf=0;
        observer.disconnect();
        document.removeEventListener('keydown',onKey);
        document.removeEventListener('visibilitychange',onVisibility);
        motionQuery.removeEventListener('change',onMotion);
    };
}
