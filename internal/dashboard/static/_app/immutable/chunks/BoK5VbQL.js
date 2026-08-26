function i(t){const n=Math.abs(t),r=n>0&&n<.01?4:2;return new Intl.NumberFormat("en-US",{style:"currency",currency:"USD",minimumFractionDigits:r,maximumFractionDigits:r}).format(t)}export{i as f};
